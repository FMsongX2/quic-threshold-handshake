#!/usr/bin/env python3
"""Summarizes one run directory produced by run.py.

Per handshake it joins the client record with the client qlog file and derives
the time of the first packet from the server, the first RTT sample and the
number of probe timeouts. Per cell it writes one row to summary.csv.
"""

import argparse
import csv
import json
import math
from pathlib import Path

RS = "\x1e"


def percentile(values: list[float], p: float) -> float:
    """Nearest-rank percentile; values must be non-empty."""
    ordered = sorted(values)
    return ordered[max(0, math.ceil(p / 100 * len(ordered)) - 1)]


def read_qlog(path: Path) -> dict:
    """Extracts per-handshake facts from one quic-go client qlog file."""
    first_sent = first_recv = hs_complete = first_rtt = None
    first_recv_type = first_recv_frames = ""
    sent_before_first_recv = sent_before_complete = recv_before_complete = 0
    initial_recv = max_pto_count = lost = 0
    for line in path.read_text().split("\n"):
        line = line.strip(RS).strip()
        if not line:
            continue
        event = json.loads(line)
        name, data, t = event.get("name"), event.get("data", {}), event.get("time")
        if name == "transport:packet_sent":
            if first_sent is None:
                first_sent = t
            if first_recv is None:
                sent_before_first_recv += 1
            if hs_complete is None:
                sent_before_complete += 1
        elif name == "transport:packet_received":
            packet_type = data.get("header", {}).get("packet_type", "")
            if first_recv is None:
                first_recv = t
                first_recv_type = packet_type
                first_recv_frames = "+".join(f.get("frame_type", "?") for f in data.get("frames", []))
            if hs_complete is None:
                recv_before_complete += 1
            if packet_type == "initial":
                initial_recv += 1
        elif name == "recovery:metrics_updated":
            if first_rtt is None and "latest_rtt" in data:
                first_rtt = data["latest_rtt"]
            max_pto_count = max(max_pto_count, data.get("pto_count", 0))
        elif name == "recovery:packet_lost":
            lost += 1
        elif name == "quic:connection_state_updated" and data.get("new") == "handshake_complete":
            hs_complete = t
    if first_sent is None:
        return {}
    return {
        "first_recv_ms": None if first_recv is None else first_recv - first_sent,
        "first_recv_type": first_recv_type,
        "first_recv_frames": first_recv_frames,
        "hs_complete_ms": None if hs_complete is None else hs_complete - first_sent,
        "first_rtt_sample_ms": first_rtt,
        "sent_before_first_recv": sent_before_first_recv,
        "sent_before_complete": sent_before_complete,
        "recv_before_complete": recv_before_complete,
        "server_initial_packets": initial_recv,
        "max_pto_count": max_pto_count,
        "packets_lost": lost,
    }


def summarize_cell(cell: Path) -> dict:
    meta = json.loads((cell / "meta.json").read_text())
    records = [json.loads(line) for line in (cell / "client.jsonl").read_text().splitlines() if line]
    measured = [r for r in records if not r["warmup"]]
    ok = [r for r in measured if r["ok"]]
    for r in ok:
        qlog = cell / "qlog" / f"{r['odcid']}_client.sqlog"
        r.update(read_qlog(qlog) if qlog.exists() else {})

    def column(key: str) -> list[float]:
        return [r[key] for r in ok if r.get(key) is not None]

    def p(key: str, q: float):
        values = column(key)
        return round(percentile(values, q), 2) if values else ""

    def mean(key: str):
        values = column(key)
        return round(sum(values) / len(values), 2) if values else ""

    first_types = sorted({f"{r['first_recv_type']}:{r['first_recv_frames']}" for r in ok if r.get("first_recv_type")})
    pto = column("max_pto_count")
    return {
        "server": meta["server"],
        "rtt_ms": meta["rtt_ms"],
        "loss_pct": meta["loss_pct"],
        "sign_delay_ms": meta["sign_delay_ms"],
        "concurrency": meta["concurrency"],
        "n": len(measured),
        "ok": len(ok),
        "t_hs_p50": p("t_hs_ms", 50),
        "t_hs_p95": p("t_hs_ms", 95),
        "t_hs_p99": p("t_hs_ms", 99),
        "t_hs_max": p("t_hs_ms", 100),
        "first_recv_p50": p("first_recv_ms", 50),
        "first_rtt_sample_p50": p("first_rtt_sample_ms", 50),
        "sent_before_first_recv_mean": mean("sent_before_first_recv"),
        "sent_before_complete_mean": mean("sent_before_complete"),
        "server_initial_packets_mean": mean("server_initial_packets"),
        "pto_share": round(sum(1 for v in pto if v > 0) / len(pto), 3) if pto else "",
        "max_pto_count": max(pto) if pto else "",
        "packets_lost_mean": mean("packets_lost"),
        "first_recv_kinds": " | ".join(first_types),
        "loadavg1_before": round(meta["loadavg_before"][0], 2),
        "loadavg1_after": round(meta["loadavg_after"][0], 2),
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_dir", type=Path)
    args = parser.parse_args()

    rows = [summarize_cell(cell) for cell in sorted(args.run_dir.iterdir()) if (cell / "meta.json").exists()]
    rows.sort(key=lambda r: (r["server"], r["rtt_ms"], r["loss_pct"], r["concurrency"], r["sign_delay_ms"]))

    # excess = median handshake time minus the zero-delay, concurrency-1 median
    # of the same server and network, minus the injected delay.
    base = {
        (r["server"], r["rtt_ms"], r["loss_pct"]): r["t_hs_p50"]
        for r in rows
        if r["sign_delay_ms"] == 0 and r["concurrency"] == 1 and r["t_hs_p50"] != ""
    }
    for r in rows:
        b = base.get((r["server"], r["rtt_ms"], r["loss_pct"]))
        r["excess_p50"] = round(r["t_hs_p50"] - b - r["sign_delay_ms"], 2) if b is not None and r["t_hs_p50"] != "" else ""

    with (args.run_dir / "summary.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)

    shown = [
        "server", "rtt_ms", "loss_pct", "concurrency", "sign_delay_ms", "ok", "n", "t_hs_p50", "t_hs_p95", "t_hs_max",
        "excess_p50", "first_recv_p50", "first_rtt_sample_p50", "sent_before_complete_mean", "pto_share", "first_recv_kinds",
    ]
    print("| " + " | ".join(shown) + " |")
    print("|" + " --- |" * len(shown))
    for r in rows:
        print("| " + " | ".join(str(r[k]) for k in shown) + " |")


if __name__ == "__main__":
    main()
