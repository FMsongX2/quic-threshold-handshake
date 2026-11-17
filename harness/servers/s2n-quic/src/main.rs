//! s2n-quic server for the handshake measurements. With `--key` the
//! certificate key is local and every signature completes after a fixed
//! delay; with `--coordd` the qth-coordd signing daemon signs. In both modes
//! a pending signature leaves other connections free to progress.

mod coordd;
mod local;

use std::io::Write;
use std::time::Duration;

use s2n_quic::provider::limits::Limits;
use s2n_quic::provider::tls::s2n_tls::certificate::OFFLOAD_PRIVATE_KEY;
use s2n_quic::Server;

struct Args {
    listen: String,
    cert: String,
    key: String,
    coordd: String,
    alpn: String,
    sign_delay: Duration,
}

fn parse_args() -> Args {
    let mut args = Args {
        listen: "0.0.0.0:4433".into(),
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
            "--listen" => args.listen = value,
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
    assert!(
        args.coordd.is_empty() || args.sign_delay.is_zero(),
        "--sign-delay-ms applies to the local key only"
    );
    args
}

fn start(args: &Args) -> Result<Server, Box<dyn std::error::Error>> {
    let limits = Limits::new().with_max_idle_timeout(Duration::from_secs(30))?;
    let builder = Server::builder().with_io(args.listen.as_str())?.with_limits(limits)?;
    let server = if args.coordd.is_empty() {
        let key = local::DelayedKey::load(&args.key, args.sign_delay)?;
        let cert = std::fs::read_to_string(&args.cert)?;
        let tls = s2n_quic::provider::tls::s2n_tls::Server::builder()
            .with_certificate(cert, OFFLOAD_PRIVATE_KEY)?
            .with_private_key_handler(key)?
            .with_application_protocols([&args.alpn])?
            .build()?;
        builder.with_tls(tls)?.start()?
    } else {
        let tls = coordd::tls_provider(&args.cert, &args.coordd, &args.alpn)?;
        builder.with_tls(tls)?.start()?
    };
    Ok(server)
}

#[tokio::main]
async fn main() {
    let args = parse_args();
    let mut server = start(&args).unwrap_or_else(|e| panic!("start server: {e}"));
    println!("ready listen={} sign_delay_ms={}", args.listen, args.sign_delay.as_millis());
    std::io::stdout().flush().unwrap();

    // Holding each connection keeps it open until the client closes it.
    while let Some(mut connection) = server.accept().await {
        tokio::spawn(async move { while let Ok(Some(_)) = connection.accept().await {} });
    }
}
