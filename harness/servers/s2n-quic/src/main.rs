//! s2n-quic server for the handshake measurements. With `--key` the
//! certificate key is local and every signature completes after a fixed
//! delay; with `--coordd` or `--coordd-digest` the qth-coordd signing daemon
//! signs. In every mode a pending signature leaves other connections free to
//! progress.
//!
//! Daemon mode picks the TLS stack by certificate key: RSA and P-256 use
//! s2n-tls and the digest listener when `--coordd-digest` is given; Ed25519,
//! or RSA and P-256 with only `--coordd`, use rustls and the message listener.

mod coordd;
mod daemon;
mod digest;
mod local;
mod rearm;

use std::io::Write;
use std::time::Duration;

use s2n_quic::provider::limits::Limits;
use s2n_quic::provider::tls::s2n_tls::callbacks::PrivateKeyCallback;
use s2n_quic::provider::tls::s2n_tls::certificate::OFFLOAD_PRIVATE_KEY;
use s2n_quic::Server;

use crate::coordd::KeyType;

struct Args {
    listen: String,
    cert: String,
    key: String,
    coordd: String,
    coordd_digest: String,
    alpn: String,
    sign_delay: Duration,
}

fn parse_args() -> Args {
    let mut args = Args {
        listen: "0.0.0.0:4433".into(),
        cert: String::new(),
        key: String::new(),
        coordd: String::new(),
        coordd_digest: String::new(),
        alpn: "qth".into(),
        sign_delay: Duration::ZERO,
    };
    let mut it = std::env::args().skip(1);
    while let Some(flag) = it.next() {
        let value = it.next().unwrap_or_else(|| panic!("{flag} needs a value"));
        match flag.as_str() {
            "--listen" => args.listen = value,
            "--cert" => args.cert = value,
            "--key" => args.key = value,
            "--coordd" => args.coordd = value,
            "--coordd-digest" => args.coordd_digest = value,
            "--alpn" => args.alpn = value,
            "--sign-delay-ms" => {
                args.sign_delay = Duration::from_millis(value.parse().expect("invalid delay"))
            },
            other => panic!("unknown flag {other}"),
        }
    }
    let daemon = !args.coordd.is_empty() || !args.coordd_digest.is_empty();
    assert!(!args.cert.is_empty(), "--cert is required");
    assert!(
        args.key.is_empty() == daemon,
        "give either --key or at least one of --coordd and --coordd-digest"
    );
    assert!(!daemon || args.sign_delay.is_zero(), "--sign-delay-ms applies to the local key only");
    args
}

/// The TLS stack serving the handshakes, reported in the ready line.
enum Tls {
    S2n,
    Rustls,
}

fn s2n_tls(
    args: &Args, key: impl PrivateKeyCallback,
) -> Result<s2n_quic::provider::tls::s2n_tls::Server, Box<dyn std::error::Error>> {
    let cert = std::fs::read_to_string(&args.cert)?;
    Ok(s2n_quic::provider::tls::s2n_tls::Server::builder()
        .with_certificate(cert, OFFLOAD_PRIVATE_KEY)?
        .with_private_key_handler(key)?
        .with_application_protocols([&args.alpn])?
        .build()?)
}

fn start(args: &Args) -> Result<(Server, Tls), Box<dyn std::error::Error>> {
    let limits = Limits::new().with_max_idle_timeout(Duration::from_secs(30))?;
    let builder = Server::builder().with_io(args.listen.as_str())?.with_limits(limits)?;
    if !args.key.is_empty() {
        let tls = s2n_tls(args, local::DelayedKey::load(&args.key, args.sign_delay)?)?;
        return Ok((builder.with_tls(tls)?.start()?, Tls::S2n));
    }
    let (chain, key_type) = coordd::load_chain(&args.cert)?;
    if key_type != KeyType::Ed25519 && !args.coordd_digest.is_empty() {
        let tls = s2n_tls(args, digest::DigestKey::connect(&args.coordd_digest, key_type)?)?;
        return Ok((builder.with_tls(tls)?.start()?, Tls::S2n));
    }
    if args.coordd.is_empty() {
        return Err(format!("--coordd is required for a certificate key of type {key_type:?}").into());
    }
    let tls = coordd::tls_provider(chain, key_type, &args.coordd, &args.alpn)?;
    Ok((builder.with_tls(tls)?.start()?, Tls::Rustls))
}

#[tokio::main]
async fn main() {
    let args = parse_args();
    let (mut server, tls) = start(&args).unwrap_or_else(|e| panic!("start server: {e}"));
    let tls = match tls {
        Tls::S2n => "s2n-tls",
        Tls::Rustls => "rustls",
    };
    println!(
        "ready listen={} sign_delay_ms={} tls={tls}",
        args.listen,
        args.sign_delay.as_millis()
    );
    std::io::stdout().flush().unwrap();

    // Holding each connection keeps it open until the client closes it.
    while let Some(mut connection) = server.accept().await {
        tokio::spawn(async move { while let Ok(Some(_)) = connection.accept().await {} });
    }
}
