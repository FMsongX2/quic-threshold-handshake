#!/usr/bin/env python3
"""Runs the signing-delay injection matrix on one Docker host.

Each cell starts one server container and one client container on a dedicated
bridge network, shapes both egress directions with tc netem, and stores the
client records, qlog files and run metadata under OUT/<cell>/.
"""

import argparse
import itertools
import json
import os
import subprocess
import sys
import time
from pathlib import Path

HARNESS = Path(__file__).resolve().parent
NETWORK = "qth-net"
SUBNET = "10.89.0.0/24"
SERVER_IP = "10.89.0.10"
SERVER_NAME = "qth-server"
CLIENT_NAME = "qth-client"
CLIENT_IMAGE = "qth-go"
CERT_ARGS = ["/certs/cert.pem", "/certs/key.pem"]

# name -> (image, Dockerfile, server command, flag that sets the signing delay)
SERVERS = {
    "quic-go": (
        "qth-go", "go.Dockerfile",
        ["qth-server-quicgo", "-cert", CERT_ARGS[0], "-key", CERT_ARGS[1]], "-sign-delay-ms",
    ),
    "aioquic": (
        "qth-aioquic", "aioquic.Dockerfile",
        ["python", "/opt/qth/server.py", "--cert", CERT_ARGS[0], "--key", CERT_ARGS[1]], "--sign-delay-ms",
    ),
    "quiche": (
        "qth-quiche", "quiche.Dockerfile",
        ["qth-server-quiche", "--cert", CERT_ARGS[0], "--key", CERT_ARGS[1]], "--sign-delay-ms",
    ),
}


def sh(*args: str, check: bool = True, capture: bool = False) -> str:
    result = subprocess.run(args, check=check, text=True, capture_output=capture)
    return result.stdout if capture else ""


def docker_quiet(*args: str) -> None:
    subprocess.run(["docker", *args], check=False, capture_output=True)


def ensure_certs(cert_dir: Path) -> None:
    if (cert_dir / "cert.pem").exists():
        return
    cert_dir.mkdir(parents=True, exist_ok=True)
    sh(
        "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-days", "30", "-subj", "/CN=server", "-addext", "subjectAltName=DNS:server",
        "-keyout", str(cert_dir / "key.pem"), "-out", str(cert_dir / "cert.pem"),
    )
    os.chmod(cert_dir / "key.pem", 0o644)


def build_images(servers: list[str]) -> dict[str, str]:
    """Builds the client image and the images of the selected servers; returns image IDs."""
    wanted = {CLIENT_IMAGE: "go.Dockerfile"} | {SERVERS[name][0]: SERVERS[name][1] for name in servers}
    for image, dockerfile in wanted.items():
        sh("docker", "build", "-q", "-t", image, "-f", str(HARNESS / "docker" / dockerfile), str(HARNESS))
    return {image: sh("docker", "image", "inspect", "-f", "{{.Id}}", image, capture=True).strip() for image in wanted}


def netem_args(rtt_ms: float, loss_pct: float) -> list[str]:
    args = ["tc", "qdisc", "replace", "dev", "eth0", "root", "netem", "delay", f"{rtt_ms / 2}ms"]
    if loss_pct > 0:
        args += ["loss", f"{loss_pct}%"]
    return args


def wait_ready(timeout_s: float = 20) -> None:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        logs = subprocess.run(["docker", "logs", SERVER_NAME], text=True, capture_output=True)
        if "ready" in logs.stdout + logs.stderr:
            return
        time.sleep(0.2)
    raise RuntimeError("server did not become ready")


def run_cell(args, out: Path, server: str, rtt: float, loss: float, delay: int, conc: int) -> None:
    cell = out / f"{server}_rtt{rtt:g}_loss{loss:g}_d{delay}_c{conc}"
    if (cell / "meta.json").exists():
        print(f"skip {cell.name}", flush=True)
        return
    (cell / "qlog").mkdir(parents=True, exist_ok=True)
    n = args.n if conc == 1 else max(args.n, conc * args.waves)
    image, _, command, delay_flag = SERVERS[server]
    docker_quiet("rm", "-f", SERVER_NAME, CLIENT_NAME)
    load_before = os.getloadavg()
    started = time.time()

    sh(
        "docker", "run", "-d", "--name", SERVER_NAME, "--network", NETWORK, "--ip", SERVER_IP,
        "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.server_cpus,
        "-v", f"{args.certs}:/certs:ro", image, *command, delay_flag, str(delay),
        capture=True,
    )
    try:
        wait_ready()
        sh("docker", "exec", SERVER_NAME, *netem_args(rtt, loss))
        client_cmd = " && ".join([
            " ".join(netem_args(rtt, loss)),
            f"qth-client -addr {SERVER_IP}:4433 -ca /certs/cert.pem -n {n} -c {conc} -out /out/client.jsonl",
        ])
        uid, gid = os.getuid(), os.getgid()
        client = subprocess.run(
            [
                "docker", "run", "--rm", "--name", CLIENT_NAME, "--network", NETWORK,
                "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.client_cpus,
                "-e", "QLOGDIR=/out/qlog", "-v", f"{args.certs}:/certs:ro", "-v", f"{cell}:/out",
                CLIENT_IMAGE, "sh", "-c", f"{client_cmd}; rc=$?; chown -R {uid}:{gid} /out; exit $rc",
            ],
            text=True, capture_output=True,
        )
        (cell / "client.log").write_text(client.stdout + client.stderr)
        server_logs = subprocess.run(["docker", "logs", SERVER_NAME], text=True, capture_output=True)
        (cell / "server.log").write_text(server_logs.stdout + server_logs.stderr)
    finally:
        docker_quiet("rm", "-f", SERVER_NAME)

    meta = {
        "server": server, "rtt_ms": rtt, "loss_pct": loss, "sign_delay_ms": delay, "concurrency": conc, "n": n,
        "client_exit_code": client.returncode, "started_unix": started, "duration_s": time.time() - started,
        "loadavg_before": load_before, "loadavg_after": os.getloadavg(),
        "server_cpus": args.server_cpus, "client_cpus": args.client_cpus,
    }
    (cell / "meta.json").write_text(json.dumps(meta, indent=2) + "\n")
    print(f"done {cell.name} exit={client.returncode} {meta['duration_s']:.1f}s", flush=True)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--servers", default="quic-go,aioquic")
    parser.add_argument("--delays", default="0", help="signing delays in ms, comma separated")
    parser.add_argument("--rtts", default="20", help="client-server RTTs in ms, comma separated")
    parser.add_argument("--losses", default="0", help="per-direction loss in percent, comma separated")
    parser.add_argument("--concs", default="1", help="handshakes per wave, comma separated")
    parser.add_argument("--n", type=int, default=50, help="measured handshakes per cell")
    parser.add_argument("--waves", type=int, default=5, help="minimum waves per cell when concurrency > 1")
    parser.add_argument("--server-cpus", default="4,5")
    parser.add_argument("--client-cpus", default="6,7")
    parser.add_argument("--certs", type=Path, default=HARNESS / "certs")
    args = parser.parse_args()
    args.certs = args.certs.resolve()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)

    servers = args.servers.split(",")
    ensure_certs(args.certs)
    images = build_images(servers)
    docker_quiet("network", "create", "--subnet", SUBNET, NETWORK)
    run_record = {"argv": sys.argv[1:], "images": images}
    (out / f"run-{int(time.time())}.json").write_text(json.dumps(run_record, indent=2) + "\n")

    matrix = itertools.product(
        servers,
        [float(v) for v in args.rtts.split(",")],
        [float(v) for v in args.losses.split(",")],
        [int(v) for v in args.delays.split(",")],
        [int(v) for v in args.concs.split(",")],
    )
    try:
        for server, rtt, loss, delay, conc in matrix:
            run_cell(args, out, server, rtt, loss, delay, conc)
    finally:
        docker_quiet("rm", "-f", SERVER_NAME, CLIENT_NAME)
        docker_quiet("network", "rm", NETWORK)


if __name__ == "__main__":
    main()
