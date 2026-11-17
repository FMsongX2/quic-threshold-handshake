//! quiche server whose certificate key signs through BoringSSL's asynchronous
//! private key method, in one of two modes. With `--key` every signature
//! completes after a fixed delay. With `--coordd` every signature is a request
//! to a qth-coordd daemon. In both modes the event loop keeps serving other
//! connections while a signature is pending.

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use boring::ex_data::Index;
use boring::hash::MessageDigest;
use boring::nid::Nid;
use boring::pkey::{Id, PKey, Private};
use boring::sign::Signer;
use boring::ssl::{
    PrivateKeyMethod, PrivateKeyMethodError, Ssl, SslContextBuilder, SslMethod, SslRef,
    SslSignatureAlgorithm,
};
use boring::x509::X509;

const MAX_DATAGRAM_SIZE: usize = 1350;
const SOCKET: mio::Token = mio::Token(0);
const WAKER: mio::Token = mio::Token(1);

struct PendingSignature {
    signature: Vec<u8>,
    ready_at: Instant,
}

struct DelayedKey {
    key: PKey<Private>,
    delay: Duration,
    pending: Index<Ssl, PendingSignature>,
    /// Times at which the event loop must retry a handshake.
    wakeups: Arc<Mutex<Vec<Instant>>>,
}

impl PrivateKeyMethod for DelayedKey {
    fn sign(
        &self, ssl: &mut SslRef, input: &[u8], signature_algorithm: SslSignatureAlgorithm,
        _output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        if signature_algorithm != SslSignatureAlgorithm::ECDSA_SECP256R1_SHA256 {
            return Err(PrivateKeyMethodError::FAILURE);
        }
        let signature = Signer::new(MessageDigest::sha256(), &self.key)
            .and_then(|mut signer| signer.sign_oneshot_to_vec(input))
            .map_err(|_| PrivateKeyMethodError::FAILURE)?;
        let ready_at = Instant::now() + self.delay;
        ssl.replace_ex_data(self.pending, PendingSignature { signature, ready_at });
        self.wakeups.lock().unwrap().push(ready_at);
        Err(PrivateKeyMethodError::RETRY)
    }

    fn decrypt(
        &self, _ssl: &mut SslRef, _input: &[u8], _output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        Err(PrivateKeyMethodError::FAILURE)
    }

    fn complete(
        &self, ssl: &mut SslRef, output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        let pending = ssl.ex_data(self.pending).ok_or(PrivateKeyMethodError::FAILURE)?;
        if Instant::now() < pending.ready_at {
            return Err(PrivateKeyMethodError::RETRY);
        }
        let signature = &pending.signature;
        if output.len() < signature.len() {
            return Err(PrivateKeyMethodError::FAILURE);
        }
        output[..signature.len()].copy_from_slice(signature);
        Ok(signature.len())
    }
}

/// Signature responses by request ID. A request has an entry from the moment
/// it is sent until its handshake takes the response or is dropped; `None`
/// means the response has not arrived.
type Responses = HashMap<u64, Option<Result<Vec<u8>, String>>>;

/// Client of the qth-coordd protocol over one TCP connection. Requests are
/// written from the event loop; a reader thread collects the responses and
/// wakes the event loop for each.
struct CoorddClient {
    stream: Mutex<TcpStream>,
    next_id: AtomicU64,
    responses: Arc<Mutex<Responses>>,
}

impl CoorddClient {
    fn connect(addr: &str, waker: Arc<mio::Waker>) -> std::io::Result<Arc<Self>> {
        let stream = TcpStream::connect(addr)?;
        stream.set_nodelay(true)?;
        let mut reader = stream.try_clone()?;
        let responses = Arc::new(Mutex::new(Responses::new()));
        let collected = responses.clone();
        std::thread::spawn(move || loop {
            let (id, response) = match read_response(&mut reader) {
                Ok(v) => v,
                Err(e) => {
                    // No signature can complete without the daemon.
                    eprintln!("coordd connection failed: {e}");
                    std::process::exit(1);
                },
            };
            if let Some(slot) = collected.lock().unwrap().get_mut(&id) {
                *slot = Some(response);
            }
            waker.wake().expect("wake event loop");
        });
        Ok(Arc::new(Self { stream: Mutex::new(stream), next_id: AtomicU64::new(1), responses }))
    }

    fn request(&self, message: &[u8]) -> std::io::Result<u64> {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let mut frame = Vec::with_capacity(12 + message.len());
        frame.extend_from_slice(&(8 + message.len() as u32).to_be_bytes());
        frame.extend_from_slice(&id.to_be_bytes());
        frame.extend_from_slice(message);
        self.responses.lock().unwrap().insert(id, None);
        if let Err(e) = self.stream.lock().unwrap().write_all(&frame) {
            self.responses.lock().unwrap().remove(&id);
            return Err(e);
        }
        Ok(id)
    }
}

fn read_response(stream: &mut TcpStream) -> std::io::Result<(u64, Result<Vec<u8>, String>)> {
    let mut size = [0; 4];
    stream.read_exact(&mut size)?;
    let mut body = vec![0; u32::from_be_bytes(size) as usize];
    stream.read_exact(&mut body)?;
    if body.len() < 9 {
        return Err(std::io::Error::other("short response"));
    }
    let id = u64::from_be_bytes(body[..8].try_into().unwrap());
    let payload = body[9..].to_vec();
    if body[8] == 0 {
        Ok((id, Ok(payload)))
    } else {
        Ok((id, Err(String::from_utf8_lossy(&payload).into_owned())))
    }
}

/// The daemon request a handshake waits for. Dropping it with the handshake
/// releases the response slot even if the response never gets taken.
struct PendingRequest {
    id: u64,
    client: Arc<CoorddClient>,
}

impl Drop for PendingRequest {
    fn drop(&mut self) {
        self.client.responses.lock().unwrap().remove(&self.id);
    }
}

struct CoorddKey {
    client: Arc<CoorddClient>,
    /// The one scheme the daemon signs with for the certificate key type.
    algorithm: SslSignatureAlgorithm,
    pending: Index<Ssl, PendingRequest>,
}

impl PrivateKeyMethod for CoorddKey {
    fn sign(
        &self, ssl: &mut SslRef, input: &[u8], signature_algorithm: SslSignatureAlgorithm,
        _output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        if signature_algorithm != self.algorithm {
            return Err(PrivateKeyMethodError::FAILURE);
        }
        let id = self.client.request(input).map_err(|e| {
            eprintln!("coordd request failed: {e}");
            PrivateKeyMethodError::FAILURE
        })?;
        ssl.replace_ex_data(self.pending, PendingRequest { id, client: self.client.clone() });
        Err(PrivateKeyMethodError::RETRY)
    }

    fn decrypt(
        &self, _ssl: &mut SslRef, _input: &[u8], _output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        Err(PrivateKeyMethodError::FAILURE)
    }

    fn complete(
        &self, ssl: &mut SslRef, output: &mut [u8],
    ) -> Result<usize, PrivateKeyMethodError> {
        let id = ssl.ex_data(self.pending).ok_or(PrivateKeyMethodError::FAILURE)?.id;
        let response = match self.client.responses.lock().unwrap().get_mut(&id) {
            Some(slot) => slot.take(),
            None => return Err(PrivateKeyMethodError::FAILURE),
        };
        match response {
            None => Err(PrivateKeyMethodError::RETRY),
            Some(Ok(signature)) if signature.len() <= output.len() => {
                output[..signature.len()].copy_from_slice(&signature);
                Ok(signature.len())
            },
            Some(Ok(_)) => Err(PrivateKeyMethodError::FAILURE),
            Some(Err(e)) => {
                eprintln!("coordd: {e}");
                Err(PrivateKeyMethodError::FAILURE)
            },
        }
    }
}

/// Returns the signature scheme the daemon uses for the certificate key and
/// its name in BoringSSL's signature algorithm list syntax.
fn coordd_algorithm(cert_path: &str) -> (SslSignatureAlgorithm, &'static str) {
    let pem = std::fs::read(cert_path).expect("read certificate");
    let key = X509::from_pem(&pem).expect("parse certificate").public_key().expect("public key");
    match key.id() {
        Id::ED25519 => (SslSignatureAlgorithm::ED25519, "ed25519"),
        Id::RSA => (SslSignatureAlgorithm::RSA_PSS_RSAE_SHA256, "rsa_pss_rsae_sha256"),
        Id::EC if key.ec_key().unwrap().group().curve_name() == Some(Nid::X9_62_PRIME256V1) => {
            (SslSignatureAlgorithm::ECDSA_SECP256R1_SHA256, "ecdsa_secp256r1_sha256")
        },
        other => panic!("unsupported certificate key type {other:?}"),
    }
}

struct Args {
    listen: SocketAddr,
    cert: String,
    key: String,
    coordd: String,
    alpn: String,
    sign_delay: Duration,
}

fn parse_args() -> Args {
    let mut args = Args {
        listen: "0.0.0.0:4433".parse().unwrap(),
        cert: String::new(),
        key: String::new(),
        coordd: String::new(),
        alpn: "qth".into(),
        sign_delay: Duration::ZERO,
    };
    let mut it = std::env::args().skip(1);
    while let Some(flag) = it.next() {
        let value = it.next().unwrap_or_else(|| panic!("{flag} needs a value"));
        match flag.as_str() {
            "--listen" => args.listen = value.parse().expect("invalid --listen"),
            "--cert" => args.cert = value,
            "--key" => args.key = value,
            "--coordd" => args.coordd = value,
            "--alpn" => args.alpn = value,
            "--sign-delay-ms" => {
                args.sign_delay = Duration::from_millis(value.parse().expect("invalid delay"))
            },
            other => panic!("unknown flag {other}"),
        }
    }
    assert!(!args.cert.is_empty(), "--cert is required");
    assert!(
        args.key.is_empty() != args.coordd.is_empty(),
        "exactly one of --key and --coordd is required"
    );
    args
}

fn main() {
    let args = parse_args();
    let wakeups = Arc::new(Mutex::new(Vec::<Instant>::new()));
    let mut poll = mio::Poll::new().unwrap();
    let waker = Arc::new(mio::Waker::new(poll.registry(), WAKER).expect("waker"));

    let mut tls = SslContextBuilder::new(SslMethod::tls()).expect("ssl context");
    tls.set_certificate_chain_file(&args.cert).expect("load certificate");
    let mode = if args.coordd.is_empty() {
        let key_pem = std::fs::read(&args.key).expect("read key");
        tls.set_private_key_method(DelayedKey {
            key: PKey::private_key_from_pem(&key_pem).expect("parse key"),
            delay: args.sign_delay,
            pending: Ssl::new_ex_index().expect("ex index"),
            wakeups: wakeups.clone(),
        });
        "local"
    } else {
        let (algorithm, name) = coordd_algorithm(&args.cert);
        // BoringSSL picks the scheme before it calls the key, so it must
        // offer only the one the daemon implements.
        tls.set_sigalgs_list(name).expect("signature algorithms");
        tls.set_private_key_method(CoorddKey {
            client: CoorddClient::connect(&args.coordd, waker.clone()).expect("connect to coordd"),
            algorithm,
            pending: Ssl::new_ex_index().expect("ex index"),
        });
        "coordd"
    };

    let mut config =
        quiche::Config::with_boring_ssl_ctx_builder(quiche::PROTOCOL_VERSION, tls).expect("config");
    config.set_application_protos(&[args.alpn.as_bytes()]).expect("alpn");
    config.set_max_idle_timeout(30_000);
    config.set_max_recv_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_max_send_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_initial_max_data(10_000_000);
    config.set_initial_max_stream_data_bidi_local(1_000_000);
    config.set_initial_max_stream_data_bidi_remote(1_000_000);
    config.set_initial_max_stream_data_uni(1_000_000);
    config.set_initial_max_streams_bidi(100);
    config.set_initial_max_streams_uni(100);

    let mut events = mio::Events::with_capacity(1024);
    let mut socket = mio::net::UdpSocket::bind(args.listen).expect("bind");
    poll.registry().register(&mut socket, SOCKET, mio::Interest::READABLE).unwrap();
    let local_addr = socket.local_addr().unwrap();

    let rng = ring::rand::SystemRandom::new();
    let conn_id_seed = ring::hmac::Key::generate(ring::hmac::HMAC_SHA256, &rng).unwrap();
    let mut clients: HashMap<quiche::ConnectionId<'static>, quiche::Connection> = HashMap::new();
    let mut buf = [0; 65535];
    let mut out = [0; MAX_DATAGRAM_SIZE];

    println!(
        "ready listen={} sign_delay_ms={} key={mode}",
        args.listen,
        args.sign_delay.as_millis()
    );

    loop {
        let now = Instant::now();
        let sign_timeout = {
            let mut wakeups = wakeups.lock().unwrap();
            wakeups.retain(|t| *t > now);
            wakeups.iter().min().map(|t| *t - now)
        };
        let timeout =
            clients.values().filter_map(|c| c.timeout()).chain(sign_timeout).min();
        poll.poll(&mut events, timeout).unwrap();

        if events.is_empty() {
            clients.values_mut().for_each(|c| c.on_timeout());
        }

        'read: while !events.is_empty() {
            let (len, from) = match socket.recv_from(&mut buf) {
                Ok(v) => v,
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => break 'read,
                Err(e) => panic!("recv failed: {e:?}"),
            };
            let pkt = &mut buf[..len];
            let Ok(hdr) = quiche::Header::from_slice(pkt, quiche::MAX_CONN_ID_LEN) else {
                continue 'read;
            };

            // Packets sent before the client learns our connection ID carry the
            // client-chosen ID, which maps to ours through the keyed hash.
            let derived = ring::hmac::sign(&conn_id_seed, &hdr.dcid);
            let derived: quiche::ConnectionId<'static> =
                derived.as_ref()[..quiche::MAX_CONN_ID_LEN].to_vec().into();
            let key = if clients.contains_key(&hdr.dcid) {
                hdr.dcid.clone().into_owned()
            } else {
                derived
            };

            if !clients.contains_key(&key) {
                if hdr.ty != quiche::Type::Initial || !quiche::version_is_supported(hdr.version) {
                    continue 'read;
                }
                let conn = quiche::accept(&key, None, local_addr, from, &mut config).expect("accept");
                clients.insert(key.clone(), conn);
            }
            let conn = clients.get_mut(&key).unwrap();
            let _ = conn.recv(pkt, quiche::RecvInfo { to: local_addr, from });
        }

        // send() retries a handshake that waits for its signature.
        for conn in clients.values_mut() {
            loop {
                let (written, info) = match conn.send(&mut out) {
                    Ok(v) => v,
                    Err(quiche::Error::Done) => break,
                    Err(_) => {
                        conn.close(false, 0x1, b"fail").ok();
                        break;
                    },
                };
                match socket.send_to(&out[..written], info.to) {
                    Ok(_) => {},
                    Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => break,
                    Err(e) => panic!("send failed: {e:?}"),
                }
            }
        }

        clients.retain(|_, c| !c.is_closed());
    }
}
