#!/usr/bin/env python3
"""Runs threshold-signer measurements on one Docker host.

bench      measures the signing delay D_sign with qth-signbench, without QUIC.
handshake  measures the handshake time T_hs with a quic-go server whose
           certificate key is the coordinator.

Every signer runs in its own container on a dedicated bridge network. The
round-trip delay to a signer is applied with tc netem on that signer's egress;
loss is applied on the egress of both the signer and the coordinator.
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

from run import CLIENT_IMAGE, HARNESS, NETWORK, SERVER_IP, SUBNET, build_images, docker_quiet, sh

SIGNER_NETWORK = "qth-sig"
SIGNER_SUBNET = "10.89.1.0/24"
SIGNER_PREFIX = "10.89.1."
COORDINATOR_IP = "10.89.1.2"
SIGNER_PORT = 7000
SIGNER_CPUS = ["0", "1", "2", "3", "8", "9", "10", "11"]
COORDINATOR_NAME = "qth-coord"
CLIENT_NAME = "qth-client"


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


def wait_ready(container: str, timeout_s: float = 20) -> None:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        logs = subprocess.run(["docker", "logs", container], text=True, capture_output=True)
        if "ready" in logs.stdout + logs.stderr:
            return
        time.sleep(0.2)
    raise RuntimeError(f"{container} did not become ready")


def netem(container: str, device: str, delay_ms: float, loss_pct: float) -> None:
    if delay_ms <= 0 and loss_pct <= 0:
        return
    command = ["docker", "exec", container, "tc", "qdisc", "replace", "dev", device, "root", "netem"]
    if delay_ms > 0:
        command += ["delay", f"{delay_ms}ms"]
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


def start_signers(keys: Path, rtts: list[float], loss: float) -> str:
    """Starts one signer per RTT entry; returns the coordinator's -signers value."""
    for i in range(1, len(rtts) + 1):
        sh(
            "docker", "run", "-d", "--name", signer_name(i), "--network", SIGNER_NETWORK, "--ip", signer_ip(i),
            "--cap-add", "NET_ADMIN", "--cpuset-cpus", SIGNER_CPUS[(i - 1) % len(SIGNER_CPUS)],
            "-v", f"{keys}:/keys:ro", CLIENT_IMAGE,
            "qth-signer", "-share", f"/keys/share-{i}.json", "-listen", f":{SIGNER_PORT}",
            capture=True,
        )
    for i, rtt in enumerate(rtts, start=1):
        wait_ready(signer_name(i))
        netem(signer_name(i), "eth0", rtt, loss)
    return ",".join(f"{signer_ip(i)}:{SIGNER_PORT}" for i in range(1, len(rtts) + 1))


def remove_all(n: int) -> None:
    docker_quiet("rm", "-f", COORDINATOR_NAME, CLIENT_NAME, "qth-server", *[signer_name(i) for i in range(1, n + 1)])


HEDGE = re.compile(r"hedge(\d*)(?:d([\d.]+))?(?:b([\d.]+))?")
NUDGE = re.compile(r"(fixed|fastest|first)n([\d.]+)")


def coordinator_flags(config: dict, policy: str, transport: str) -> list[str]:
    """policy is fixed, fastest or first, optionally followed by nF, or hedge[N][dF][bB].
    transport is per-request or persistent[K].

    nF pings the signers still silent after F times the expected round trip.
    hedge engages all signers and hedgeN the N nearest. dF delays widening by
    F times the expected response time of the t nearest signers. bB lets at
    most the share B of signatures widen. persistentK keeps K connections per
    signer.
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
    if transport.startswith("persistent"):
        flags += ["-transport", "persistent", "-signer-conns", transport[len("persistent"):] or "1"]
    else:
        flags += ["-transport", transport]
    if config["precompute"]:
        flags.append("-precompute")
    return flags


def cell_name(config: dict, policy: str, transport: str, rtt_spec: str, loss: float) -> str:
    scheme = config["scheme"] + ("-pre" if config["precompute"] else "")
    rtt = rtt_spec.replace("/", "-")
    return f"{scheme}_t{config['t']}n{config['n']}_{policy}_{transport}_rtt{rtt}_loss{loss:g}"


def base_meta(config: dict, policy: str, transport: str, rtts: list[float], loss: float) -> dict:
    return {
        **config, "policy": policy, "transport": transport, "signer_rtts_ms": rtts, "signer_loss_pct": loss,
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
    meta = base_meta(config, policy, transport, rtts, loss)
    try:
        signers = start_signers(keys, rtts, loss)
        bench = " ".join([
            "qth-signbench", "-public", "/keys/public.json", "-signers", signers, "-n", str(args.n),
            "-out", "/out/sign.jsonl", *coordinator_flags(config, policy, transport),
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
    meta = base_meta(config, policy, transport, rtts, loss)
    meta.update(client_rtt_ms=args.client_rtt, concurrency=args.conc)
    uid, gid = os.getuid(), os.getgid()
    try:
        signers = start_signers(keys, rtts, loss)
        # The server needs both networks before it starts, because the
        # coordinator connects to the signers at startup.
        sh(
            "docker", "create", "--name", "qth-server", "--network", NETWORK, "--ip", SERVER_IP,
            "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.server_cpus,
            "-v", f"{keys}:/keys:ro", "-v", f"{cell}:/out", CLIENT_IMAGE,
            "qth-server-quicgo", "-cert", "/keys/cert.pem", "-threshold-public", "/keys/public.json",
            "-signers", signers, "-trace-out", "/out/server-trace.jsonl", *coordinator_flags(config, policy, transport),
            capture=True,
        )
        sh("docker", "network", "connect", "--ip", COORDINATOR_IP, SIGNER_NETWORK, "qth-server")
        sh("docker", "start", "qth-server", capture=True)
        wait_ready("qth-server")
        netem("qth-server", device_for("qth-server", "10.89.0."), args.client_rtt / 2, 0)
        netem("qth-server", device_for("qth-server", SIGNER_PREFIX), 0, loss)

        client = " && ".join([
            f"tc qdisc replace dev eth0 root netem delay {args.client_rtt / 2}ms",
            f"qth-client -addr {SERVER_IP}:4433 -ca /keys/cert.pem -n {args.n} -c {args.conc} -out /out/client.jsonl",
        ])
        result = subprocess.run(
            [
                "docker", "run", "--rm", "--name", CLIENT_NAME, "--network", NETWORK,
                "--cap-add", "NET_ADMIN", "--cpuset-cpus", args.client_cpus,
                "-e", "QLOGDIR=/out/qlog", "-v", f"{keys}:/keys:ro", "-v", f"{cell}:/out", CLIENT_IMAGE,
                "sh", "-c", f"{client}; rc=$?; chown -R {uid}:{gid} /out; exit $rc",
            ],
            text=True, capture_output=True,
        )
        (cell / "client.log").write_text(result.stdout + result.stderr)
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
    parser.add_argument("--conc", type=int, default=1, help="handshakes per wave (handshake mode)")
    parser.add_argument("--server-cpus", default="4,5")
    parser.add_argument("--client-cpus", default="6,7")
    parser.add_argument("--keys", type=Path, default=HARNESS / "keys")
    args = parser.parse_args()
    args.keys = args.keys.resolve()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)

    images = build_images([])
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
