//! quiche server whose certificate key completes every signature after a
//! fixed delay through BoringSSL's asynchronous private key method. The event
//! loop keeps serving other connections while a signature is pending.

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use boring::ex_data::Index;
use boring::hash::MessageDigest;
use boring::pkey::{PKey, Private};
use boring::sign::Signer;
use boring::ssl::{
    PrivateKeyMethod, PrivateKeyMethodError, Ssl, SslContextBuilder, SslMethod, SslRef,
    SslSignatureAlgorithm,
};

const MAX_DATAGRAM_SIZE: usize = 1350;

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

struct Args {
    listen: SocketAddr,
    cert: String,
    key: String,
    alpn: String,
    sign_delay: Duration,
}

fn parse_args() -> Args {
    let mut args = Args {
        listen: "0.0.0.0:4433".parse().unwrap(),
        cert: String::new(),
        key: String::new(),
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
            "--alpn" => args.alpn = value,
            "--sign-delay-ms" => {
                args.sign_delay = Duration::from_millis(value.parse().expect("invalid delay"))
            },
            other => panic!("unknown flag {other}"),
        }
    }
    assert!(!args.cert.is_empty() && !args.key.is_empty(), "--cert and --key are required");
    args
}

fn main() {
    let args = parse_args();
    let wakeups = Arc::new(Mutex::new(Vec::<Instant>::new()));

    let key_pem = std::fs::read(&args.key).expect("read key");
    let mut tls = SslContextBuilder::new(SslMethod::tls()).expect("ssl context");
    tls.set_certificate_chain_file(&args.cert).expect("load certificate");
    tls.set_private_key_method(DelayedKey {
        key: PKey::private_key_from_pem(&key_pem).expect("parse key"),
        delay: args.sign_delay,
        pending: Ssl::new_ex_index().expect("ex index"),
        wakeups: wakeups.clone(),
    });

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

    let mut poll = mio::Poll::new().unwrap();
    let mut events = mio::Events::with_capacity(1024);
    let mut socket = mio::net::UdpSocket::bind(args.listen).expect("bind");
    poll.registry().register(&mut socket, mio::Token(0), mio::Interest::READABLE).unwrap();
    let local_addr = socket.local_addr().unwrap();

    let rng = ring::rand::SystemRandom::new();
    let conn_id_seed = ring::hmac::Key::generate(ring::hmac::HMAC_SHA256, &rng).unwrap();
    let mut clients: HashMap<quiche::ConnectionId<'static>, quiche::Connection> = HashMap::new();
    let mut buf = [0; 65535];
    let mut out = [0; MAX_DATAGRAM_SIZE];

    println!("ready listen={} sign_delay_ms={}", args.listen, args.sign_delay.as_millis());

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
