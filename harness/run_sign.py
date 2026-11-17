#!/usr/bin/env python3
"""Runs threshold-signer measurements on one Docker host.

bench      measures the signing delay D_sign with qth-signbench, without QUIC.
handshake  measures the handshake time T_hs with a QUIC server whose
           certificate key is the coordinator. quic-go can hold the
           coordinator in its own process; every server can reach it through
           a qth-coordd daemon in the same container.

Every signer runs in its own container on a dedicated bridge network. The
round-trip delay to a signer is applied with tc netem on that signer's egress;
loss is applied on the egress of both the signer and the coordinator.

Handshake mode can change a signer while the client runs: --events takes
AT:ACTION:SIGNER[:VALUE] items separated by semicolons, where AT is seconds
after the client starts and ACTION is stop, pause, unpause, rtt (ms) or loss
(percent).
"""

import argparse
import itertools
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

from run import CLIENT_IMAGE, CLIENTS, HARNESS, NETWORK, SERVER_IP, SUBNET, build_images, docker_quiet, sh

SIGNER_NETWORK = "qth-sig"
SIGNER_SUBNET = "10.89.1.0/24"
SIGNER_PREFIX = "10.89.1."
COORDINATOR_IP = "10.89.1.2"
SIGNER_PORT = 7000  # plain TCP, and QUIC over UDP
SIGNER_TLS_PORT = 7001
# One core per signer in this order. CPU 9 is left out: an unrelated process
# on the measurement host keeps it busy.
SIGNER_CPUS = ["0", "1", "2", "3", "8", "10", "11"]
COORDINATOR_NAME = "qth-coord"
CLIENT_NAME = "qth-client"
DAEMON_ADDR = "127.0.0.1:7100"
# Front servers of handshake mode: name -> (image, Dockerfile, server command
# that signs through the daemon). quic-go without a command holds the
# coordinator in its own process.
SERVERS = {
    "quic-go": ("qth-go", "go.Dockerfile", None),
    "quic-go-coordd": ("qth-go", "go.Dockerfile", ["qth-server-quicgo", "-cert", "/keys/cert.pem", "-coordd", DAEMON_ADDR]),
    "aioquic": (
        "qth-aioquic", "aioquic.Dockerfile",
        ["python3", "/opt/qth/server.py", "--cert", "/keys/cert.pem", "--coordd", DAEMON_ADDR],
    ),
    "quiche": ("qth-quiche", "quiche.Dockerfile", ["qth-server-quiche", "--cert", "/keys/cert.pem", "--coordd", DAEMON_ADDR]),
    "s2n-quic": ("qth-s2n", "s2n-quic.Dockerfile", ["qth-server-s2n", "--cert", "/keys/cert.pem", "--coordd", DAEMON_ADDR]),
}


def signer_name(i: int) -> str:
    return f"qth-signer-{i}"


def signer_ip(i: int) -> str:
    return f"{SIGNER_PREFIX}{10 + i}"


def parse_config(token: str) -> dict:
    """scheme:t:n or scheme:t:n:pre"""
    parts = token.split(":")
    return {"scheme": parts[0], "t": int(parts[1]), "n": int(parts[2]), "precompute": parts[3:] == ["pre"]}


def parse_rtts(spec: str, n: int) -> list[float]:
    """A single value applies to every signer; a/b/c lists one value per signer."""
    values = [float(v) for v in spec.split("/")]
    if len(values) == 1:
        values *= n
    if len(values) != n:
        raise ValueError(f"RTT spec {spec} does not match {n} signers")
    return values


def ensure_keys(keys_root: Path, config: dict) -> Path:
    keys = keys_root / f"{config['scheme']}_t{config['t']}n{config['n']}"
    if not (keys / "public.json").exists():
        keys.mkdir(parents=True, exist_ok=True)
        sh(
            "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", "-v", f"{keys}:/keys", CLIENT_IMAGE,
            "qth-dealer", "-scheme", config["scheme"], "-t", str(config["t"]), "-n", str(config["n"]), "-out", "/keys",
        )
    return keys


def wait_ready(container: str, timeout_s: float = 60) -> None:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        logs = subprocess.run(["docker", "logs", container], text=True, capture_output=True)
        if "ready" in logs.stdout + logs.stderr:
            return
        time.sleep(0.2)
    raise RuntimeError(f"{container} did not become ready")


def netem(container: str, device: str, delay_ms: float, loss_pct: float, jitter_ms: float = 0) -> None:
    if delay_ms <= 0 and loss_pct <= 0:
        return
    command = ["docker", "exec", container, "tc", "qdisc", "replace", "dev", device, "root", "netem"]
    if delay_ms > 0:
        command += ["delay", f"{delay_ms}ms"]
        if jitter_ms > 0:
            command += [f"{jitter_ms}ms", "distribution", "normal"]
    if loss_pct > 0:
        command += ["loss", f"{loss_pct}%"]
    sh(*command)


def device_for(container: str, prefix: str) -> str:
    """Name of the interface of container whose address starts with prefix."""
    out = sh("docker", "exec", container, "ip", "-o", "-4", "addr", "show", capture=True)
    for line in out.splitlines():
        fields = line.split()
        if fields[3].startswith(prefix):
            return fields[1]
    raise RuntimeError(f"{container} has no address in {prefix}")


def start_signers(args, keys: Path, rtts: list[float], loss: float, transport: str) -> str:
    """Starts one signer per RTT entry; returns the coordinator's -signers value for transport."""
    if len(rtts) * args.signer_cpus > len(SIGNER_CPUS):
        raise ValueError(f"{len(rtts)} signers with {args.signer_cpus} CPUs each need more than {len(SIGNER_CPUS)} CPUs")
    for i in range(1, len(rtts) + 1):
        cpus = ",".join(SIGNER_CPUS[(i - 1) * args.signer_cpus:i * args.signer_cpus])
        sh(
            "docker", "run", "-d", "--name", signer_name(i), "--network", SIGNER_NETWORK, "--ip", signer_ip(i),
            "--cap-add", "NET_ADMIN", "--cpuset-cpus", cpus,
            "-v", f"{keys}:/keys:ro", CLIENT_IMAGE,
            "qth-signer", "-share", f"/keys/share-{i}.json", "-listen", f":{SIGNER_PORT}",
            "-tls-listen", f":{SIGNER_TLS_PORT}", "-quic-listen", f":{SIGNER_PORT}",
            capture=True,
        )
    for i, rtt in enumerate(rtts, start=1):
        wait_ready(signer_name(i))
        netem(signer_name(i), "eth0", rtt, loss, rtt * args.signer_jitter_pct / 100)
    port = SIGNER_TLS_PORT if transport.startswith("tls") else SIGNER_PORT
    return ",".join(f"{signer_ip(i)}:{port}" for i in range(1, len(rtts) + 1))


def parse_events(spec: str) -> list[tuple[float, str, int, float]]:
    """AT:ACTION:SIGNER[:VALUE] items separated by semicolons, sorted by AT."""
    events = []
    for item in filter(None, spec.split(";")):
        fields = item.split(":")
        if fields[1] not in ("stop", "pause", "unpause", "rtt", "loss"):
            raise ValueError(f"unknown event action in {item}")
        events.append((float(fields[0]), fields[1], int(fields[2]), float(fields[3]) if len(fields) > 3 else 0.0))
    return sorted(events)


def apply_event(event: tuple[float, str, int, float], rtts: list[float], losses: list[float]) -> None:
    """Changes one signer. rtts and losses hold the current link state per
    signer and are updated, because netem replaces the whole qdisc."""
    _, action, signer, value = event
    name = signer_name(signer)
    if action == "stop":
        sh("docker", "kill", name, capture=True)
    elif action in ("pause", "unpause"):
        sh("docker", action, name, capture=True)
    else:
        if action == "rtt":
            rtts[signer - 1] = value
        else:
            losses[signer - 1] = value
        sh("docker", "exec", name, "tc", "qdisc", "del", "dev", "eth0", "root", check=False, capture=True)
        netem(name, "eth0", rtts[signer - 1], losses[signer - 1])


def remove_all(n: int) -> None:
    docker_quiet("rm", "-f", COORDINATOR_NAME, CLIENT_NAME, "qth-server", *[signer_name(i) for i in range(1, n + 1)])


HEDGE = re.compile(r"hedge(\d*)(?:d([\d.]+))?(?:b([\d.]+))?")
NUDGE = re.compile(r"(fixed|fastest|first)n([\d.]+)")
POOLED = re.compile(r"(persistent|tls|quic)(\d*)")


def coordinator_flags(config: dict, policy: str, transport: str) -> list[str]:
    """policy is fixed, fastest or first, optionally followed by nF, or hedge[N][dF][bB].
    transport is persistent[K], tls[K] or quic[K], or per-request,
    tls-per-request, quic-per-request or quic-0rtt.

    nF pings the signers still silent after F times the expected round trip.
    hedge engages all signers and hedgeN the N nearest. dF delays widening by
    F times the expected response time of the t nearest signers. bB lets at
    most the share B of signatures widen. A transport followed by K keeps K
    connections per signer.
    """
    flags = ["-policy", policy]
    if nudge := NUDGE.fullmatch(policy):
        flags = ["-policy", nudge[1], "-nudge-delay-factor", nudge[2]]
    if hedge := HEDGE.fullmatch(policy):
        width, delay, budget = hedge.groups()
        flags = [
            "-policy", "hedge", "-hedge-width", width or "0", "-hedge-delay-factor", delay or "0",
            "-hedge-budget", budget or "0",
        ]
    if pooled := POOLED.fullmatch(transport):
        flags += ["-transport", pooled[1], "-signer-conns", pooled[2] or "1"]
    else:
        flags += ["-transport", transport]
    if config["precompute"]:
        flags.append("-precompute")
    return flags


def signer_qlog_flags(args, cell: Path) -> list[str]:
    """Flags that make the coordinator write the qlog of its QUIC signer
    connections into the cell, which the container sees as /out."""
    if not args.signer_qlog:
        return []
    (cell / "signer-qlog").mkdir(exist_ok=True)
    return ["-signer-qlog", "/out/signer-qlog"]


def cell_name(config: dict, policy: str, transport: str, rtt_spec: str, loss: float) -> str:
    scheme = config["scheme"] + ("-pre" if config["precompute"] else "")
    rtt = rtt_spec.replace("/", "-")
    return f"{scheme}_t{config['t']}n{config['n']}_{policy}_{transport}_rtt{rtt}_loss{loss:g}"


def base_meta(args, config: dict, policy: str, transport: str, rtts: list[float], loss: float) -> dict:
    return {
        **config, "policy": policy, "transport": transport, "signer_rtts_ms": rtts, "signer_loss_pct": loss,
        "signer_cpus": args.signer_cpus, "signer_jitter_pct": args.signer_jitter_pct,
        "started_unix": time.time(), "loadavg_before": os.getloadavg(),
    }


def finish(cell: Path, meta: dict, exit_code: int) -> None:
    meta.update(exit_code=exit_code, duration_s=time.time() - meta["started_unix"], loadavg_after=os.getloadavg())
    (cell / "meta.json").write_text(json.dumps(meta, indent=2) + "\n")
    print(f"done {cell.name} exit={exit_code} {meta['duration_s']:.1f}s", flush=True)


def run_bench(args, out: Path, config: dict, policy: str, transport: str, rtt_spec: str, loss: float) -> None:
    cell = out / cell_name(config, policy, transport, rtt_spec, loss)
    if (cell / "meta.json").exists():
        print(f"skip {cell.name}", flush=True)
        return
    cell.mkdir(parents=True, exist_ok=True)
    keys = ensure_keys(args.keys, config)
    rtts = parse_rtts(rtt_spec, config["n"])
    remove_all(config["n"])
    meta = base_meta(args, config, policy, transport, rtts, loss)
    try:
        signers = start_signers(args, keys, rtts, loss, transport)
        bench = " ".join([
            "qth-signbench", "-public", "/keys/public.json", "-signers", signers, "-n", str(args.n),
            "-out", "/out/sign.jsonl", *coordinator_flags(config, policy, transport), *signer_qlog_flags(args, cell),
        ])
        shape = f"tc qdisc replace dev eth0 root netem loss {loss}% && " if loss > 0 else ""
        result = subprocess.run(
            [
                "docker", "run", "--rm", "--name", COORDINATOR_NAME, "--network", SIGNER_NETWORK, "--ip", COORDINATOR_IP,
                "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.server_cpus,
                "-v", f"{keys}:/keys:ro", "-v", f"{cell}:/out", CLIENT_IMAGE,
                "sh", "-c", f"{shape}{bench}; rc=$?; chown -R {os.getuid()}:{os.getgid()} /out; exit $rc",
            ],
            text=True, capture_output=True,
        )
        (cell / "bench.log").write_text(result.stdout + result.stderr)
    finally:
        remove_all(config["n"])
    finish(cell, meta, result.returncode)


def run_handshake(args, out: Path, config: dict, policy: str, transport: str, rtt_spec: str, loss: float) -> None:
    cell = out / cell_name(config, policy, transport, rtt_spec, loss)
    if (cell / "meta.json").exists():
        print(f"skip {cell.name}", flush=True)
        return
    (cell / "qlog").mkdir(parents=True, exist_ok=True)
    keys = ensure_keys(args.keys, config)
    rtts = parse_rtts(rtt_spec, config["n"])
    remove_all(config["n"])
    meta = base_meta(args, config, policy, transport, rtts, loss)
    meta.update(
        client_rtt_ms=args.client_rtt, client_loss_pct=args.client_loss, concurrency=args.conc, rate=args.rate,
        server_cpus=args.server_cpus, client_cpus=args.client_cpus, events=args.events,
        server=args.server, client=args.client,
    )
    server_image, _, daemon_server = SERVERS[args.server]
    client_image, _, client_program = CLIENTS[args.client]
    uid, gid = os.getuid(), os.getgid()
    try:
        signers = start_signers(args, keys, rtts, loss, transport)
        # The server needs both networks before it starts, because the
        # coordinator connects to the signers at startup.
        coordinator = [
            "-threshold-public", "/keys/public.json", "-signers", signers, "-trace-out", "/out/server-trace.jsonl",
            *coordinator_flags(config, policy, transport), *signer_qlog_flags(args, cell),
        ]
        if daemon_server is None:
            command = ["qth-server-quicgo", "-cert", "/keys/cert.pem", *coordinator]
        else:
            command = ["serve-with-coordd", *coordinator, "--", *daemon_server]
        sh(
            "docker", "create", "--name", "qth-server", "--network", NETWORK, "--ip", SERVER_IP,
            "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.server_cpus,
            "-v", f"{keys}:/keys:ro", "-v", f"{cell}:/out", server_image, *command,
            capture=True,
        )
        sh("docker", "network", "connect", "--ip", COORDINATOR_IP, SIGNER_NETWORK, "qth-server")
        sh("docker", "start", "qth-server", capture=True)
        wait_ready("qth-server")
        netem("qth-server", device_for("qth-server", "10.89.0."), args.client_rtt / 2, args.client_loss)
        netem("qth-server", device_for("qth-server", SIGNER_PREFIX), 0, loss)

        # A fixed rate has no waves; qlog files of every connection would
        # cost the client more than the handshakes themselves.
        pacing = f"-rate {args.rate}" if args.rate > 0 else f"-c {args.conc}"
        client = " && ".join([
            " ".join(["tc", "qdisc", "replace", "dev", "eth0", "root", "netem", "delay", f"{args.client_rtt / 2}ms"]
                     + (["loss", f"{args.client_loss}%"] if args.client_loss > 0 else [])),
            f"{client_program} -addr {SERVER_IP}:4433 -ca /keys/cert.pem -n {args.n} {pacing} -out /out/client.jsonl",
        ])
        events = parse_events(args.events)
        link_rtts, link_losses = list(rtts), [loss] * len(rtts)
        while events and events[0][0] <= 0:
            apply_event(events.pop(0), link_rtts, link_losses)
        process = subprocess.Popen(
            [
                "docker", "run", "--rm", "--name", CLIENT_NAME, "--network", NETWORK,
                "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.client_cpus,
                *([] if args.rate > 0 else ["-e", "QLOGDIR=/out/qlog"]),
                "-v", f"{keys}:/keys:ro", "-v", f"{cell}:/out", client_image,
                "sh", "-c", f"{client}; rc=$?; chown -R {uid}:{gid} /out; exit $rc",
            ],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        client_started = time.time()
        meta["client_started_unix"] = client_started
        for event in events:
            time.sleep(max(0.0, client_started + event[0] - time.time()))
            if process.poll() is None:
                apply_event(event, link_rtts, link_losses)
        output, _ = process.communicate()
        result = subprocess.CompletedProcess(process.args, process.returncode, output, "")
        (cell / "client.log").write_text(result.stdout)
        logs = subprocess.run(["docker", "logs", "qth-server"], text=True, capture_output=True)
        (cell / "server.log").write_text(logs.stdout + logs.stderr)
        subprocess.run(["docker", "exec", "qth-server", "chown", "-R", f"{uid}:{gid}", "/out"], capture_output=True)
    finally:
        remove_all(config["n"])
    finish(cell, meta, result.returncode)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("mode", choices=["bench", "handshake"])
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--configs", required=True, help="scheme:t:n[:pre], comma separated")
    parser.add_argument("--policies", default="fixed")
    parser.add_argument("--transports", default="persistent")
    parser.add_argument("--rtts", default="0", help="signer RTTs in ms; a/b/c gives one value per signer")
    parser.add_argument("--losses", default="0", help="per-direction loss on signer links in percent")
    parser.add_argument("--n", type=int, default=200, help="measured signatures or handshakes per cell")
    parser.add_argument("--client-rtt", type=float, default=20, help="client-server RTT in ms (handshake mode)")
    parser.add_argument("--client-loss", type=float, default=0, help="per-direction loss on the client link in percent (handshake mode)")
    parser.add_argument("--conc", type=int, default=1, help="handshakes per wave (handshake mode)")
    parser.add_argument("--rate", type=float, default=0, help="handshakes started per second regardless of completions; replaces waves (handshake mode)")
    parser.add_argument("--events", default="", help="signer changes while the client runs; see the module description (handshake mode)")
    parser.add_argument("--signer-cpus", type=int, default=1, help="CPU cores per signer")
    parser.add_argument("--signer-jitter-pct", type=float, default=0, help="standard deviation of the signer link delay, in percent of its RTT")
    parser.add_argument("--server-cpus", default="4,5")
    parser.add_argument("--client-cpus", default="6,7")
    parser.add_argument("--keys", type=Path, default=HARNESS / "keys")
    parser.add_argument("--signer-qlog", action="store_true", help="write qlog files of the QUIC signer connections")
    parser.add_argument("--server", default="quic-go", choices=sorted(SERVERS), help="front server (handshake mode)")
    parser.add_argument("--client", default="quic-go", choices=sorted(CLIENTS), help="client (handshake mode)")
    args = parser.parse_args()
    args.keys = args.keys.resolve()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)

    images = build_images([], args.client)
    server_image, server_dockerfile, _ = SERVERS[args.server]
    if server_image not in images:
        sh("docker", "build", "-q", "-t", server_image, "-f", str(HARNESS / "docker" / server_dockerfile), str(HARNESS))
        images[server_image] = sh("docker", "image", "inspect", "-f", "{{.Id}}", server_image, capture=True).strip()
    docker_quiet("network", "create", "--subnet", SIGNER_SUBNET, SIGNER_NETWORK)
    if args.mode == "handshake":
        docker_quiet("network", "create", "--subnet", SUBNET, NETWORK)
    (out / f"run-{int(time.time())}.json").write_text(json.dumps({"argv": sys.argv[1:], "images": images}, indent=2) + "\n")

    matrix = itertools.product(
        [parse_config(token) for token in args.configs.split(",")],
        args.policies.split(","),
        args.transports.split(","),
        args.rtts.split(","),
        [float(v) for v in args.losses.split(",")],
    )
    run = run_bench if args.mode == "bench" else run_handshake
    max_signers = 0
    try:
        for config, policy, transport, rtt_spec, loss in matrix:
            if policy.startswith("first") and config["scheme"] != "rsa":
                continue
            if policy.startswith("hedge") and config["scheme"] != "frost":
                continue
            max_signers = max(max_signers, config["n"])
            run(args, out, config, policy, transport, rtt_spec, loss)
    finally:
        remove_all(max_signers)
        docker_quiet("network", "rm", SIGNER_NETWORK)
        docker_quiet("network", "rm", NETWORK)


if __name__ == "__main__":
    main()
