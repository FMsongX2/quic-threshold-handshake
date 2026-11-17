//! Daemon mode over rustls, used for Ed25519 certificates and for RSA and
//! P-256 certificates without a digest listener.
//!
//! The daemon's message listener expects the unhashed CertificateVerify
//! input, which rustls hands to its signer (s2n-tls only exposes the digest,
//! see digest.rs). rustls signs synchronously, so the TLS offload provider
//! runs each connection's TLS session on its own thread: a pending signature
//! blocks only that thread, and the QUIC endpoint keeps serving every other
//! connection.

use std::future::Future;
use std::sync::Arc;

use rustls::pki_types::pem::PemObject;
use rustls::pki_types::CertificateDer;
use rustls::sign::{CertifiedKey, Signer, SigningKey, SingleCertAndKey};
use rustls::{SignatureAlgorithm, SignatureScheme};
use s2n_quic::provider::tls::offload::{Executor, Offload, OffloadBuilder};

use crate::daemon::Daemon;
use crate::rearm::Rearm;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KeyType {
    Ed25519,
    Rsa,
    P256,
}

impl KeyType {
    /// The TLS signature scheme of the daemon contract for this key type.
    fn scheme(self) -> (SignatureScheme, SignatureAlgorithm) {
        match self {
            KeyType::Ed25519 => (SignatureScheme::ED25519, SignatureAlgorithm::ED25519),
            KeyType::Rsa => (SignatureScheme::RSA_PSS_SHA256, SignatureAlgorithm::RSA),
            KeyType::P256 => (SignatureScheme::ECDSA_NISTP256_SHA256, SignatureAlgorithm::ECDSA),
        }
    }
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

pub type Provider =
    Rearm<Offload<s2n_quic::provider::tls::rustls::Server, ThreadPerSession, ()>>;

/// Builds a TLS provider that presents `chain` and has the daemon's message
/// listener at `daemon_addr` sign with its key.
pub fn tls_provider(
    chain: Vec<CertificateDer<'static>>, key_type: KeyType, daemon_addr: &str, alpn: &str,
) -> Result<Provider, String> {
    let (scheme, algorithm) = key_type.scheme();
    let key = DaemonKey { daemon: Daemon::connect(daemon_addr)?, scheme, algorithm };
    let certified = CertifiedKey::new(chain, Arc::new(key));

    // The resolver is the only way to supply a key that rustls cannot load.
    #[allow(deprecated)]
    let server = s2n_quic::provider::tls::rustls::Server::builder()
        .with_cert_resolver(Arc::new(SingleCertAndKey::from(certified)))
        .and_then(|b| b.with_application_protocols([alpn].iter()))
        .and_then(|b| b.build())
        .map_err(|e| format!("rustls server: {e}"))?;
    Ok(Rearm(
        OffloadBuilder::new()
            .with_endpoint(server)
            .with_executor(ThreadPerSession(tokio::runtime::Handle::current()))
            .build(),
    ))
}

/// Reads the certificate chain in `path` and the key type of its leaf.
pub fn load_chain(path: &str) -> Result<(Vec<CertificateDer<'static>>, KeyType), String> {
    let chain = CertificateDer::pem_file_iter(path)
        .and_then(|certs| certs.collect::<Result<Vec<_>, _>>())
        .map_err(|e| format!("read {path}: {e}"))?;
    let leaf = chain.first().ok_or_else(|| format!("no certificate in {path}"))?;
    let key_type = key_type(leaf)?;
    Ok((chain, key_type))
}

const OID_ED25519: &[u8] = &[0x2b, 0x65, 0x70];
const OID_RSA: &[u8] = &[0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01];
const OID_EC: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01];
const OID_P256: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07];

fn key_type(cert: &[u8]) -> Result<KeyType, String> {
    let (oid, params) =
        spki_algorithm(cert).ok_or("cannot parse the certificate's public key algorithm")?;
    match oid {
        OID_ED25519 => Ok(KeyType::Ed25519),
        OID_RSA => Ok(KeyType::Rsa),
        OID_EC if matches!(der(params), Some((0x06, OID_P256, _))) => Ok(KeyType::P256),
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
