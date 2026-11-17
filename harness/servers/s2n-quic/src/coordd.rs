//! Daemon mode: the certificate key lives behind the qth-coordd signing daemon.
//!
//! s2n-tls passes its private key callback only the SHA-256 digest of the
//! CertificateVerify input, while the daemon protocol carries the unhashed
//! input (Ed25519 cannot sign a digest at all). This mode therefore uses the
//! rustls provider, whose signer receives the unhashed input. rustls signs
//! synchronously, so the TLS offload provider runs each connection's TLS
//! session on its own thread: a pending signature blocks only that thread,
//! and the QUIC endpoint keeps serving every other connection.

use std::collections::HashMap;
use std::fmt;
use std::future::Future;
use std::io::{Read, Write};
use std::net::{Shutdown, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::Duration;

use s2n_quic::provider::tls::offload::{Executor, Offload, OffloadBuilder};
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::CertificateDer;
use rustls::sign::{CertifiedKey, Signer, SigningKey, SingleCertAndKey};
use rustls::{SignatureAlgorithm, SignatureScheme};

/// Longest a handshake thread waits for one signature before failing it.
const SIGN_TIMEOUT: Duration = Duration::from_secs(30);
/// Bound on a response frame, far above any signature the daemon returns.
const MAX_FRAME: usize = 1 << 20;

type Reply = mpsc::Sender<Result<Vec<u8>, String>>;

/// Client of the signing daemon. All handshakes share one TCP connection and
/// keep their requests in flight concurrently; a broken connection fails its
/// pending requests and is replaced by the next request.
pub struct Daemon {
    addr: String,
    next_id: AtomicU64,
    link: Mutex<Option<Arc<Link>>>,
}

struct Link {
    writer: Mutex<TcpStream>,
    /// Waiting requests by id; `None` once the connection has failed.
    pending: Mutex<Option<HashMap<u64, Reply>>>,
}

impl fmt::Debug for Daemon {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Daemon").field("addr", &self.addr).finish()
    }
}

impl Daemon {
    /// Connects at once so that an unreachable daemon stops the server at startup.
    pub fn connect(addr: &str) -> Result<Arc<Self>, String> {
        let daemon =
            Arc::new(Self { addr: addr.into(), next_id: AtomicU64::new(0), link: Mutex::new(None) });
        daemon.link()?;
        Ok(daemon)
    }

    fn link(&self) -> Result<Arc<Link>, String> {
        let mut slot = self.link.lock().unwrap();
        if let Some(link) = slot.as_ref().filter(|link| link.is_open()) {
            return Ok(link.clone());
        }
        let stream =
            TcpStream::connect(&self.addr).map_err(|e| format!("connect {}: {e}", self.addr))?;
        stream.set_nodelay(true).map_err(|e| e.to_string())?;
        let reader = stream.try_clone().map_err(|e| e.to_string())?;
        let link = Arc::new(Link {
            writer: Mutex::new(stream),
            pending: Mutex::new(Some(HashMap::new())),
        });
        let receiver = link.clone();
        std::thread::spawn(move || receiver.read_responses(reader));
        *slot = Some(link.clone());
        Ok(link)
    }

    /// Returns the daemon's signature over `message`, the unhashed
    /// CertificateVerify input.
    pub fn sign(&self, message: &[u8]) -> Result<Vec<u8>, String> {
        let link = self.link()?;
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (reply, response) = mpsc::channel();
        link.pending
            .lock()
            .unwrap()
            .as_mut()
            .ok_or("daemon connection closed")?
            .insert(id, reply);

        let body_len = u32::try_from(8 + message.len()).map_err(|_| "message too long")?;
        let mut frame = Vec::with_capacity(12 + message.len());
        frame.extend_from_slice(&body_len.to_be_bytes());
        frame.extend_from_slice(&id.to_be_bytes());
        frame.extend_from_slice(message);
        let written = link.writer.lock().unwrap().write_all(&frame);
        if let Err(e) = written {
            link.fail(&format!("daemon write: {e}"));
        }

        let result = response.recv_timeout(SIGN_TIMEOUT);
        if result.is_err() {
            if let Some(pending) = link.pending.lock().unwrap().as_mut() {
                pending.remove(&id);
            }
        }
        result.map_err(|_| format!("no daemon response within {SIGN_TIMEOUT:?}"))?
    }
}

impl Link {
    fn is_open(&self) -> bool {
        self.pending.lock().unwrap().is_some()
    }

    fn read_responses(&self, mut stream: TcpStream) {
        let error = loop {
            match read_response(&mut stream) {
                Ok((id, result)) => {
                    let reply = self.pending.lock().unwrap().as_mut().and_then(|p| p.remove(&id));
                    if let Some(reply) = reply {
                        let _ = reply.send(result);
                    }
                },
                Err(e) => break e,
            }
        };
        self.fail(&format!("daemon connection: {error}"));
    }

    fn fail(&self, reason: &str) {
        let pending = self.pending.lock().unwrap().take();
        for reply in pending.into_iter().flat_map(HashMap::into_values) {
            let _ = reply.send(Err(reason.into()));
        }
        // Wakes the reader thread when the failure was seen by a writer.
        let _ = self.writer.lock().unwrap().shutdown(Shutdown::Both);
    }
}

fn read_response(stream: &mut TcpStream) -> Result<(u64, Result<Vec<u8>, String>), String> {
    let mut len = [0; 4];
    stream.read_exact(&mut len).map_err(|e| e.to_string())?;
    let len = u32::from_be_bytes(len) as usize;
    if !(9..=MAX_FRAME).contains(&len) {
        return Err(format!("invalid response length {len}"));
    }
    let mut body = vec![0; len];
    stream.read_exact(&mut body).map_err(|e| e.to_string())?;
    let id = u64::from_be_bytes(body[..8].try_into().unwrap());
    let payload = body[9..].to_vec();
    let result = match body[8] {
        0 => Ok(payload),
        1 => Err(format!("daemon: {}", String::from_utf8_lossy(&payload))),
        status => Err(format!("daemon returned unknown status {status}")),
    };
    Ok((id, result))
}

#[derive(Debug)]
struct DaemonKey {
    daemon: Arc<Daemon>,
    scheme: SignatureScheme,
    algorithm: SignatureAlgorithm,
}

impl SigningKey for DaemonKey {
    fn choose_scheme(&self, offered: &[SignatureScheme]) -> Option<Box<dyn Signer>> {
        offered.contains(&self.scheme).then(|| {
            Box::new(DaemonSigner { daemon: self.daemon.clone(), scheme: self.scheme })
                as Box<dyn Signer>
        })
    }

    fn algorithm(&self) -> SignatureAlgorithm {
        self.algorithm
    }
}

#[derive(Debug)]
struct DaemonSigner {
    daemon: Arc<Daemon>,
    scheme: SignatureScheme,
}

impl Signer for DaemonSigner {
    fn sign(&self, message: &[u8]) -> Result<Vec<u8>, rustls::Error> {
        // The client only sees a handshake failure, so the cause is logged here.
        self.daemon.sign(message).map_err(|e| {
            eprintln!("sign: {e}");
            rustls::Error::General(e)
        })
    }

    fn scheme(&self) -> SignatureScheme {
        self.scheme
    }
}

/// Runs every TLS session on a thread of its own, so that a signature that
/// blocks inside rustls stalls only its own handshake.
pub struct ThreadPerSession(tokio::runtime::Handle);

impl Executor for ThreadPerSession {
    fn spawn(&self, task: impl Future<Output = ()> + Send + 'static) {
        let runtime = self.0.clone();
        std::thread::spawn(move || runtime.block_on(task));
    }
}

pub type Provider = Offload<s2n_quic::provider::tls::rustls::Server, ThreadPerSession, ()>;

/// Builds a TLS provider that presents the certificate chain in `cert_path`
/// and has the daemon at `daemon_addr` sign with its key.
pub fn tls_provider(cert_path: &str, daemon_addr: &str, alpn: &str) -> Result<Provider, String> {
    let chain = CertificateDer::pem_file_iter(cert_path)
        .and_then(|certs| certs.collect::<Result<Vec<_>, _>>())
        .map_err(|e| format!("read {cert_path}: {e}"))?;
    let leaf = chain.first().ok_or_else(|| format!("no certificate in {cert_path}"))?;
    let (scheme, algorithm) = signature_scheme(leaf)?;
    let key = DaemonKey { daemon: Daemon::connect(daemon_addr)?, scheme, algorithm };
    let certified = CertifiedKey::new(chain, Arc::new(key));

    // The resolver is the only way to supply a key that rustls cannot load.
    #[allow(deprecated)]
    let server = s2n_quic::provider::tls::rustls::Server::builder()
        .with_cert_resolver(Arc::new(SingleCertAndKey::from(certified)))
        .and_then(|b| b.with_application_protocols([alpn].iter()))
        .and_then(|b| b.build())
        .map_err(|e| format!("rustls server: {e}"))?;
    Ok(OffloadBuilder::new()
        .with_endpoint(server)
        .with_executor(ThreadPerSession(tokio::runtime::Handle::current()))
        .build())
}

const OID_ED25519: &[u8] = &[0x2b, 0x65, 0x70];
const OID_RSA: &[u8] = &[0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01];
const OID_EC: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01];
const OID_P256: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07];

/// Picks the TLS signature scheme of the contract for the certificate's key.
fn signature_scheme(cert: &[u8]) -> Result<(SignatureScheme, SignatureAlgorithm), String> {
    let (oid, params) =
        spki_algorithm(cert).ok_or("cannot parse the certificate's public key algorithm")?;
    match oid {
        OID_ED25519 => Ok((SignatureScheme::ED25519, SignatureAlgorithm::ED25519)),
        OID_RSA => Ok((SignatureScheme::RSA_PSS_SHA256, SignatureAlgorithm::RSA)),
        OID_EC if matches!(der(params), Some((0x06, OID_P256, _))) => {
            Ok((SignatureScheme::ECDSA_NISTP256_SHA256, SignatureAlgorithm::ECDSA))
        },
        _ => Err("certificate key is not Ed25519, RSA or P-256".into()),
    }
}

/// Returns the algorithm OID and parameters of the certificate's
/// SubjectPublicKeyInfo.
fn spki_algorithm(cert: &[u8]) -> Option<(&[u8], &[u8])> {
    let (_, cert, _) = der(cert)?;
    let (_, mut tbs, _) = der(cert)?;
    if tbs.first() == Some(&0xa0) {
        tbs = der(tbs)?.2;
    }
    // serialNumber, signature, issuer, validity, subject
    for _ in 0..5 {
        tbs = der(tbs)?.2;
    }
    let (_, spki, _) = der(tbs)?;
    let (_, algorithm, _) = der(spki)?;
    let (_, oid, params) = der(algorithm)?;
    Some((oid, params))
}

/// Splits one DER element into tag, contents and the bytes after it.
fn der(input: &[u8]) -> Option<(u8, &[u8], &[u8])> {
    let (&tag, rest) = input.split_first()?;
    let (&first, rest) = rest.split_first()?;
    let (len, rest) = if first < 0x80 {
        (first as usize, rest)
    } else {
        let n = (first & 0x7f) as usize;
        if n == 0 || n > 4 || rest.len() < n {
            return None;
        }
        (rest[..n].iter().fold(0, |len, &b| len << 8 | b as usize), &rest[n..])
    };
    (rest.len() >= len).then(|| (tag, &rest[..len], &rest[len..]))
}
