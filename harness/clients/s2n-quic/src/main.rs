//! Command qth-client-s2n opens QUIC connections in waves with s2n-quic and
//! records how long each full handshake takes. Its flags and JSON-lines
//! records are those of the Go qth-client, so either client can drive a run.

mod verify;

use std::io::{BufWriter, Write};
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use rustls::ClientConfig;
use s2n_quic::client::Connect;
use s2n_quic::provider::limits::Limits;
use s2n_quic::Client;
use serde::Serialize;
use tokio::sync::watch;
use tokio::task::JoinHandle;

#[derive(Serialize)]
struct Record {
    #[serde(rename = "i")]
    index: usize,
    wave: i64,
    warmup: bool,
    start_unix_ns: i64,
    t_hs_ms: f64,
    ok: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    err: String,
    /// s2n-quic does not expose the original destination connection ID.
    odcid: String,
}

impl Record {
    fn new(index: usize, wave: i64, warmup: bool) -> Self {
        Self {
            index,
            wave,
            warmup,
            start_unix_ns: 0,
            t_hs_ms: 0.0,
            ok: false,
            err: String::new(),
            odcid: String::new(),
        }
    }
}

struct Args {
    addr: String,
    sni: String,
    ca: String,
    alpn: String,
    n: usize,
    conc: usize,
    warmup: usize,
    timeout: Duration,
    gap: Duration,
    out: String,
}

fn fatal(msg: impl std::fmt::Display) -> ! {
    eprintln!("{msg}");
    std::process::exit(1)
}

fn usage(msg: impl std::fmt::Display) -> ! {
    eprintln!("{msg}");
    std::process::exit(2)
}

const USAGE: &str = "Usage of qth-client-s2n:
  -addr string        server address (host:port)
  -alpn string        ALPN protocol (default \"qth\")
  -c int              handshakes started at the same instant per wave (default 1)
  -ca string          PEM file with the trusted server certificate
  -gap duration       pause between waves (default 100ms)
  -n int              number of measured handshakes (default 50)
  -out string         JSON lines output file
  -sni string         TLS server name (default \"server\")
  -timeout duration   per-handshake timeout (default 30s)
  -warmup int         handshakes run before measuring, excluded from statistics (default 1)";

/// Parses flags the way Go's flag package does: `-name value`,
/// `-name=value`, either with one or two dashes, up to the first non-flag.
fn parse_args() -> Args {
    let mut args = Args {
        addr: String::new(),
        sni: "server".into(),
        ca: String::new(),
        alpn: "qth".into(),
        n: 50,
        conc: 1,
        warmup: 1,
        timeout: Duration::from_secs(30),
        gap: Duration::from_millis(100),
        out: String::new(),
    };
    let mut it = std::env::args().skip(1);
    while let Some(arg) = it.next() {
        if arg == "--" {
            break;
        }
        let Some(flag) = arg.strip_prefix("--").or_else(|| arg.strip_prefix('-')) else {
            break;
        };
        if flag == "h" || flag == "help" {
            eprintln!("{USAGE}");
            std::process::exit(0);
        }
        let (name, inline) = match flag.split_once('=') {
            Some((name, value)) => (name.to_string(), Some(value.to_string())),
            None => (flag.to_string(), None),
        };
        let value = inline
            .or_else(|| it.next())
            .unwrap_or_else(|| usage(format!("flag needs an argument: -{name}")));
        let invalid = |e: &dyn std::fmt::Display| -> ! {
            usage(format!("invalid value {value:?} for flag -{name}: {e}"))
        };
        match name.as_str() {
            "addr" => args.addr = value.clone(),
            "sni" => args.sni = value.clone(),
            "ca" => args.ca = value.clone(),
            "alpn" => args.alpn = value.clone(),
            "out" => args.out = value.clone(),
            "n" => args.n = value.parse().unwrap_or_else(|e| invalid(&e)),
            "c" => args.conc = value.parse().unwrap_or_else(|e| invalid(&e)),
            "warmup" => args.warmup = value.parse().unwrap_or_else(|e| invalid(&e)),
            "timeout" => args.timeout = parse_duration(&value).unwrap_or_else(|e| invalid(&e)),
            "gap" => args.gap = parse_duration(&value).unwrap_or_else(|e| invalid(&e)),
            _ => usage(format!("flag provided but not defined: -{name}")),
        }
    }
    if args.addr.is_empty() || args.ca.is_empty() || args.out.is_empty() {
        fatal("-addr, -ca and -out are required");
    }
    if args.conc == 0 {
        usage("-c must be positive");
    }
    args
}

/// Parses a Go duration string such as "30s", "1.5s" or "1m30s".
fn parse_duration(s: &str) -> Result<Duration, String> {
    if s == "0" {
        return Ok(Duration::ZERO);
    }
    if s.is_empty() {
        return Err("empty duration".into());
    }
    let mut total = 0f64;
    let mut rest = s;
    while !rest.is_empty() {
        let number_len =
            rest.find(|c: char| !(c.is_ascii_digit() || c == '.')).unwrap_or(rest.len());
        let number: f64 = rest[..number_len].parse().map_err(|_| format!("invalid duration {s}"))?;
        rest = &rest[number_len..];
        let unit_len = rest.find(|c: char| c.is_ascii_digit() || c == '.').unwrap_or(rest.len());
        let seconds = match &rest[..unit_len] {
            "ns" => 1e-9,
            "us" | "µs" | "μs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => return Err(format!("invalid unit in duration {s}")),
        };
        rest = &rest[unit_len..];
        total += number * seconds;
    }
    Ok(Duration::from_secs_f64(total))
}

/// UDP payload size of the Initial datagrams, that of quic-go.
const INITIAL_DATAGRAM_SIZE: u16 = 1280;

struct Dialer {
    addr: SocketAddr,
    tls: Arc<ClientConfig>,
    sni: String,
    timeout: Duration,
}

impl Dialer {
    /// Creates a client endpoint with its own socket for one connection, as
    /// the Go client dials each connection from a new socket.
    fn endpoint(&self) -> Result<Client, String> {
        let tls = s2n_quic::provider::tls::rustls::Client::from(self.tls.clone());
        let limits = Limits::new()
            .with_max_handshake_duration(self.timeout)
            .map_err(|e| e.to_string())?;
        // quic-go sends 1280-byte Initial datagrams; the same size gives the
        // server the same anti-amplification budget as with the Go client.
        let (bind, ip_udp_header) =
            if self.addr.is_ipv4() { ("0.0.0.0:0", 28) } else { ("[::]:0", 48) };
        let io = s2n_quic::provider::io::tokio::Builder::default()
            .with_receive_address(bind.parse().unwrap())
            .and_then(|b| b.with_initial_mtu(INITIAL_DATAGRAM_SIZE + ip_udp_header))
            .and_then(|b| b.build())
            .map_err(|e| e.to_string())?;
        Client::builder()
            .with_tls(tls)
            .map_err(|e| e.to_string())?
            .with_io(io)
            .map_err(|e| e.to_string())?
            .with_limits(limits)
            .map_err(|e| e.to_string())?
            .start()
            .map_err(|e| e.to_string())
    }

    /// Runs one handshake and returns its record, plus a task that keeps the
    /// endpoint alive until the closed connection has drained.
    async fn handshake(
        &self, endpoint: Result<Client, String>, mut rec: Record,
    ) -> (Record, Option<JoinHandle<()>>) {
        let connect = Connect::new(self.addr).with_server_name(self.sni.as_str());
        let start_wall = SystemTime::now();
        let start = Instant::now();
        let result = match &endpoint {
            Ok(client) => match tokio::time::timeout(self.timeout, client.connect(connect)).await {
                Ok(Ok(conn)) => Ok(conn),
                Ok(Err(e)) => Err(e.to_string()),
                Err(_) => Err("handshake timeout".into()),
            },
            Err(e) => Err(e.clone()),
        };
        let elapsed = start.elapsed();
        rec.start_unix_ns = start_wall.duration_since(UNIX_EPOCH).unwrap().as_nanos() as i64;
        rec.t_hs_ms = elapsed.as_nanos() as f64 / 1e6;
        match result {
            Ok(conn) => {
                rec.ok = true;
                conn.close(0u32.into());
            },
            Err(e) => rec.err = e,
        }
        let drain = endpoint.ok().map(|mut client| {
            tokio::spawn(async move {
                let _ = client.wait_idle().await;
            })
        });
        (rec, drain)
    }
}

fn write_record(out: &mut impl Write, rec: &Record) {
    serde_json::to_writer(&mut *out, rec).unwrap_or_else(|e| fatal(e));
    out.write_all(b"\n").unwrap_or_else(|e| fatal(e));
}

#[tokio::main]
async fn main() {
    let args = parse_args();
    let tls = verify::client_config(&args.ca, &args.alpn).unwrap_or_else(|e| fatal(e));
    let addr = tokio::net::lookup_host(&args.addr)
        .await
        .unwrap_or_else(|e| fatal(e))
        .next()
        .unwrap_or_else(|| fatal(format!("no address for {}", args.addr)));
    let dialer = Arc::new(Dialer { addr, tls, sni: args.sni.clone(), timeout: args.timeout });
    let file = std::fs::File::create(&args.out).unwrap_or_else(|e| fatal(e));
    let mut out = BufWriter::new(file);
    let mut drains = Vec::new();

    let mut index = 0;
    for _ in 0..args.warmup {
        let (rec, drain) =
            dialer.handshake(dialer.endpoint(), Record::new(index, -1, true)).await;
        write_record(&mut out, &rec);
        drains.extend(drain);
        index += 1;
        tokio::time::sleep(args.gap).await;
    }

    let mut wave = 0;
    while index - args.warmup < args.n {
        let size = args.conc.min(args.n - (index - args.warmup));
        // Endpoints are created before the gate opens, so the wave measures
        // only the handshakes, which all start at the same instant.
        let (open, gate) = watch::channel(false);
        let tasks: Vec<_> = (0..size)
            .map(|k| {
                let endpoint = dialer.endpoint();
                let rec = Record::new(index + k, wave, false);
                let dialer = dialer.clone();
                let mut gate = gate.clone();
                tokio::spawn(async move {
                    let _ = gate.wait_for(|open| *open).await;
                    dialer.handshake(endpoint, rec).await
                })
            })
            .collect();
        open.send_replace(true);
        for task in tasks {
            let (rec, drain) = task.await.unwrap_or_else(|e| fatal(e));
            write_record(&mut out, &rec);
            drains.extend(drain);
        }
        out.flush().unwrap_or_else(|e| fatal(e));
        index += size;
        wave += 1;
        tokio::time::sleep(args.gap).await;
    }
    out.flush().unwrap_or_else(|e| fatal(e));

    // Let closed connections send their CONNECTION_CLOSE before exiting.
    let _ = tokio::time::timeout(args.timeout, async {
        for drain in drains {
            let _ = drain.await;
        }
    })
    .await;
}
