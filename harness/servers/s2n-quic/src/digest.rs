//! Daemon mode over s2n-tls for RSA and P-256 certificates. s2n-tls hands
//! the SHA-256 digest of the CertificateVerify input to its asynchronous
//! private key callback, which forwards it to the daemon's digest listener
//! and completes the operation when the signature arrives. The endpoint keeps
//! driving other connections in the meantime, as in local key mode.
//!
//! s2n-tls picks the signature scheme by its own preference order, which
//! lists the SHA-256 variant of ECDSA and of RSA-PSS (rsae) first, so a client
//! that offers it always gets it. An operation with any other hash fails,
//! since the digest listener accepts only SHA-256 digests.

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};

use s2n_quic::provider::tls::s2n_tls::callbacks::{
    ConnectionFuture, OperationType, PrivateKeyCallback, PrivateKeyOperation,
};
use s2n_quic::provider::tls::s2n_tls::connection::Connection;
use s2n_quic::provider::tls::s2n_tls::enums::{HashAlgorithm, SignatureAlgorithm};
use s2n_quic::provider::tls::s2n_tls::error::Error;

use crate::coordd::KeyType;
use crate::daemon::{Daemon, PendingSignature};

pub struct DigestKey {
    daemon: Arc<Daemon>,
    algorithm: SignatureAlgorithm,
}

impl DigestKey {
    pub fn connect(addr: &str, key_type: KeyType) -> Result<Self, String> {
        let algorithm = match key_type {
            KeyType::Rsa => SignatureAlgorithm::RSA_PSS_RSAE,
            KeyType::P256 => SignatureAlgorithm::ECDSA,
            KeyType::Ed25519 => return Err("an Ed25519 key cannot sign a digest".into()),
        };
        Ok(Self { daemon: Daemon::connect(addr)?, algorithm })
    }
}

fn failure(message: String) -> Error {
    // The client only sees a handshake failure, so the cause is logged here.
    eprintln!("sign: {message}");
    Error::application(message.into())
}

impl PrivateKeyCallback for DigestKey {
    fn handle_operation(
        &self, _connection: &mut Connection, operation: PrivateKeyOperation,
    ) -> Result<Option<Pin<Box<dyn ConnectionFuture>>>, Error> {
        match operation.kind()? {
            OperationType::Sign(algorithm, HashAlgorithm::SHA256) if *algorithm == self.algorithm => {},
            kind => return Err(failure(format!("unsupported operation {kind:?} for this key"))),
        }
        let mut digest = vec![0; operation.input_size()?];
        operation.input(&mut digest)?;
        Ok(Some(Box::pin(DaemonSignature {
            operation: Some(operation),
            signature: self.daemon.sign_async(&digest),
        })))
    }
}

struct DaemonSignature {
    operation: Option<PrivateKeyOperation>,
    signature: PendingSignature,
}

impl ConnectionFuture for DaemonSignature {
    fn poll(
        mut self: Pin<&mut Self>, connection: &mut Connection, cx: &mut Context,
    ) -> Poll<Result<(), Error>> {
        let signature = match Pin::new(&mut self.signature).poll(cx) {
            Poll::Pending => return Poll::Pending,
            Poll::Ready(signature) => signature.map_err(failure)?,
        };
        let operation = self.operation.take().expect("polled after completion");
        Poll::Ready(operation.set_output(connection, &signature))
    }
}
