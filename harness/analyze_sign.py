#!/usr/bin/env python3
"""Summarizes a run directory produced by run_sign.py.

For every signature the trace gives the total signing delay and, per round,
the wall time and each signer's reported compute time. The summary splits the
delay into signer compute on the critical path, the rest of the round time
(network and queueing), and coordinator-local work.
"""

import argparse
import csv
import json
from pathlib import Path

from analyze import percentile, read_lines

MS = 1e6


def load_traces(path: Path) -> list[dict]:
    return [json.loads(line) for line in read_lines(path)]


def decompose(trace: dict) -> dict:
    rounds = trace["rounds"]
    round_ns = sum(r["wall_ns"] for r in rounds)
    compute_ns = sum(max(c["compute_ns"] for c in r["calls"]) for r in rounds)
    return {
        "d_sign": trace["total_ns"] / MS,
        "rounds": len(rounds),
        "compute": compute_ns / MS,
        "network": (round_ns - compute_ns) / MS,
        "hedge_wait": trace.get("hedge_wait_ns", 0) / MS,
        "local": (trace["total_ns"] - round_ns - trace.get("hedge_wait_ns", 0)) / MS,
    }


def model_network_ms(meta: dict) -> float:
    """Network term of the delay model without loss: the number of round trips
    times the largest RTT among the signers the policy selects."""
    rtts = meta["signer_rtts_ms"]
    chosen = rtts[: meta["t"]] if meta["policy"].startswith("fixed") else sorted(rtts)[: meta["t"]]
    rounds = 2 if meta["scheme"] == "frost" and not meta["precompute"] else 1
    trips_per_round = 2 if meta["transport"] == "per-request" else 1
    return rounds * trips_per_round * max(chosen)


def summarize(cell: Path) -> dict:
    meta = json.loads((cell / "meta.json").read_text())
    handshake = any((cell / name).exists() for name in ("client.jsonl", "client.jsonl.gz"))
    traces = load_traces(cell / ("server-trace.jsonl" if handshake else "sign.jsonl"))
    parts = [decompose(t) for t in traces]

    def p(key: str, q: float):
        values = [part[key] for part in parts]
        return round(percentile(values, q), 3) if values else ""

    row = {
        "scheme": meta["scheme"] + ("-pre" if meta["precompute"] else ""),
        "t": meta["t"],
        "n": meta["n"],
        "policy": meta["policy"],
        "transport": meta["transport"],
        "signer_rtts_ms": "/".join(f"{v:g}" for v in meta["signer_rtts_ms"]),
        "signer_loss_pct": meta["signer_loss_pct"],
        "signatures": len(parts),
        "d_sign_p50": p("d_sign", 50),
        "d_sign_p95": p("d_sign", 95),
        "d_sign_p99": p("d_sign", 99),
        "d_sign_max": p("d_sign", 100),
        "rounds_p50": p("rounds", 50),
        "compute_p50": p("compute", 50),
        "network_p50": p("network", 50),
        "local_p50": p("local", 50),
        "model_network_ms": model_network_ms(meta),
        "nudged_share": round(sum(1 for t in traces if t.get("nudged")) / len(traces), 3) if traces else "",
        "hedge_fired_share": round(sum(1 for t in traces if t.get("hedge_fired")) / len(traces), 3) if traces else "",
        "hedge_sessions_mean": round(sum(t.get("hedge_sessions", 0) for t in traces) / len(traces), 2) if traces else "",
        "shares_per_signature_mean": round(
            sum(t.get("hedge_shares") or (meta["n"] if meta["policy"].startswith("first") else meta["t"]) for t in traces)
            / len(traces), 2
        ) if traces else "",
        "pool_miss_share": round(sum(1 for t in traces if t.get("pool_miss")) / len(traces), 3) if traces else "",
        "used_signers": " ".join(
            f"{signer}:{count}" for signer, count in sorted(
                {s: sum(1 for t in traces if s in {c["signer"] for c in t["rounds"][-1]["calls"]})
                 for s in range(1, meta["n"] + 1)}.items()
            ) if count
        ),
        "exit_code": meta["exit_code"],
        "loadavg1_before": round(meta["loadavg_before"][0], 2),
    }
    if handshake:
        records = [json.loads(line) for line in read_lines(cell / "client.jsonl")]
        ok = [r["t_hs_ms"] for r in records if not r["warmup"] and r["ok"]]
        row["client_rtt_ms"] = meta["client_rtt_ms"]
        row["handshakes"] = len([r for r in records if not r["warmup"]])
        row["handshakes_ok"] = len(ok)
        row["t_hs_p50"] = round(percentile(ok, 50), 3) if ok else ""
        row["t_hs_p95"] = round(percentile(ok, 95), 3) if ok else ""
        row["t_hs_p99"] = round(percentile(ok, 99), 3) if ok else ""
        if ok and parts:
            row["t_hs_minus_d_sign_p50"] = round(row["t_hs_p50"] - row["d_sign_p50"], 3)
    return row


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_dir", type=Path)
    args = parser.parse_args()

    rows = [summarize(cell) for cell in sorted(args.run_dir.iterdir()) if (cell / "meta.json").exists()]
    fields = list(dict.fromkeys(key for row in rows for key in row))
    with (args.run_dir / "summary.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=fields, restval="")
        writer.writeheader()
        writer.writerows(rows)

    hidden = {"exit_code", "loadavg1_before", "d_sign_max", "pool_miss_share"}
    shown = [field for field in fields if field not in hidden]
    print("| " + " | ".join(shown) + " |")
    print("|" + " --- |" * len(shown))
    for row in rows:
        print("| " + " | ".join(str(row.get(field, "")) for field in shown) + " |")


if __name__ == "__main__":
    main()
