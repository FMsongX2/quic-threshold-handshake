#!/usr/bin/env python3
"""Summarizes the repeated measurements written by campaigns/repeat.sh.

usage: analyze_repeat.py RUN_DIR

RUN_DIR holds the stage directories policies, newplace, handshake and
clientloss; a missing stage is skipped. Each stage prints a Markdown table
and writes a CSV file into RUN_DIR.

A percentile of one run uses the nearest-rank rule of analyze.py. Across the
runs of one condition the script reports the mean, the smallest and the
largest value, and a 95% Student-t interval of the mean. For a proportion it
reports a 95% Wilson interval.
"""

import argparse
import csv
import json
import math
import statistics
from pathlib import Path

from analyze import percentile, read_lines

# Two-sided 95% quantiles of Student's t by degrees of freedom.
T95 = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571, 6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262}
# Milliseconds a QUIC timer adds to 2r when it repairs a loss (see model.py).
QUIC_TIMER_MS = 27.0
TIE_MS = 0.5


def records(path: Path) -> list[dict]:
    return [json.loads(line) for line in read_lines(path)]


def across(values: list[float]) -> dict:
    """Mean, range and 95% t-interval half-width of per-run values."""
    out = {"runs": len(values), "mean": statistics.mean(values), "min": min(values), "max": max(values), "half": float("nan")}
    if len(values) > 1:
        out["half"] = T95[len(values) - 1] * statistics.stdev(values) / math.sqrt(len(values))
    return out


def wilson(successes: int, n: int) -> tuple[float, float]:
    z = 1.959964
    p = successes / n
    centre = (p + z * z / (2 * n)) / (1 + z * z / n)
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / (1 + z * z / n)
    return centre - half, centre + half


def table(header: list[str], body: list[list]) -> None:
    print("| " + " | ".join(header) + " |")
    print("|" + " --- |" * len(header))
    for row in body:
        print("| " + " | ".join(str(v) for v in row) + " |")
    print()


def write_csv(path: Path, header: list[str], body: list[list]) -> None:
    with path.open("w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(header)
        writer.writerows(body)


def bench_runs(stage: Path) -> dict[tuple, list[list[float]]]:
    """D_sign in ms per run, keyed by (scheme, t, placement, policy)."""
    runs: dict[tuple, list[list[float]]] = {}
    for rep in sorted(stage.glob("rep*")):
        for cell in sorted(p for p in rep.iterdir() if (p / "meta.json").exists()):
            meta = json.loads((cell / "meta.json").read_text())
            if meta["exit_code"] != 0:
                print(f"excluded {cell}: exit code {meta['exit_code']}")
                continue
            key = (
                meta["scheme"] + ("-pre" if meta["precompute"] else ""), meta["t"],
                "/".join(f"{r:g}" for r in meta["signer_rtts_ms"]), meta["policy"],
            )
            runs.setdefault(key, []).append([r["total_ns"] / 1e6 for r in records(cell / "sign.jsonl")])
    return runs


def policies(stage: Path, out: Path) -> None:
    header = ["scheme", "t", "policy", "runs", "signatures", "p50_mean", "p95_mean", "p99_mean", "p99_min", "p99_max",
              "p99_ci95_half", "p99_pooled"]
    body = []
    for (scheme, t, _, policy), runs in sorted(bench_runs(stage).items()):
        p99 = across([percentile(run, 99) for run in runs])
        pooled = [v for run in runs for v in run]
        body.append([
            scheme, t, policy, p99["runs"], len(pooled),
            f"{statistics.mean(percentile(run, 50) for run in runs):.1f}",
            f"{statistics.mean(percentile(run, 95) for run in runs):.1f}",
            f"{p99['mean']:.1f}", f"{p99['min']:.1f}", f"{p99['max']:.1f}", f"{p99['half']:.1f}",
            f"{percentile(pooled, 99):.1f}",
        ])
    print("## policies: D_sign in ms on placement 5/5/20/20/80, loss 2%")
    table(header, body)
    write_csv(out / "policies.csv", header, body)


def newplace(stage: Path, out: Path, predictions: Path) -> None:
    predicted = {}
    with predictions.open() as f:
        for row in csv.DictReader(f):
            if row["transport"] == "quic":
                predicted[(row["placement"], int(row["t"]), row["policy"])] = float(row["d_sign_p99"])
    groups: dict[tuple, dict[str, list[list[float]]]] = {}
    for (_, t, placement, policy), runs in bench_runs(stage).items():
        groups.setdefault((placement, t), {})[policy] = runs

    header = ["placement", "t", "policy", "runs", "p50_pooled", "p99_pooled", "p99_per_run", "model_p99"]
    body, verdicts = [], []
    for (placement, t), by_policy in sorted(groups.items()):
        pooled_p99 = {}
        for policy, runs in sorted(by_policy.items()):
            pooled = [v for run in runs for v in run]
            pooled_p99[policy] = percentile(pooled, 99)
            body.append([
                placement, t, policy, len(runs), f"{percentile(pooled, 50):.1f}", f"{pooled_p99[policy]:.1f}",
                " ".join(f"{percentile(run, 99):.1f}" for run in runs), f"{predicted[(placement, t, policy)]:.1f}",
            ])
        rtts = sorted(float(v) for v in placement.split("/"))
        rule = min(rtts[t], 2 * rtts[t - 1] + QUIC_TIMER_MS)
        best_policy = min(pooled_p99, key=pooled_p99.get)
        best = pooled_p99[best_policy]
        lowest_model = min(predicted[(placement, t, p)] for p in pooled_p99)
        first = [p for p in pooled_p99 if predicted[(placement, t, p)] - lowest_model <= TIE_MS]
        regret = min(pooled_p99[p] for p in first) - best
        nudge_gap = pooled_p99["fastestn1.5"] - pooled_p99["fastest"]
        verdicts.append([
            placement, t, f"{rule:.0f}", best_policy, f"{best:.1f}", f"{best - rule:+.1f}", 0 <= best - rule <= 10,
            " ".join(first), f"{regret:.1f}", regret <= 5, f"{rtts[t - 1]:g}", f"{nudge_gap:+.1f}",
        ])
    print("## newplace: D_sign in ms, two runs pooled, loss 2%")
    table(header, body)
    verdict_header = ["placement", "t", "rule_ms", "measured_best", "best_p99", "best_minus_rule", "rule_holds",
                      "model_first", "regret_of_model_first", "model_holds", "rtt_of_tth", "nudge_minus_fastest_p99"]
    table(verdict_header, verdicts)
    write_csv(out / "newplace.csv", header, body)
    write_csv(out / "newplace-verdicts.csv", verdict_header, verdicts)


def handshake(stage: Path, out: Path) -> None:
    runs: dict[tuple, list[dict]] = {}
    for rep in sorted(stage.glob("rep*")):
        for cell in sorted(p for p in rep.iterdir() if (p / "meta.json").exists()):
            meta = json.loads((cell / "meta.json").read_text())
            measured = [r for r in records(cell / "client.jsonl") if not r["warmup"]]
            ok = [r["t_hs_ms"] for r in measured if r["ok"]]
            d_sign = [r["total_ns"] / 1e6 for r in records(cell / "server-trace.jsonl")]
            runs.setdefault((meta["scheme"], meta["t"], meta["policy"]), []).append({
                "n": len(measured), "ok": len(ok), "t50": percentile(ok, 50), "t95": percentile(ok, 95),
                "t99": percentile(ok, 99), "d50": percentile(d_sign, 50), "d99": percentile(d_sign, 99),
            })
    header = ["scheme", "t", "policy", "runs", "handshakes", "ok", "t_hs_p50_mean", "t_hs_p95_mean", "t_hs_p99_mean",
              "t_hs_p99_min", "t_hs_p99_max", "t_hs_p99_ci95_half", "d_sign_p50_mean", "d_sign_p99_mean"]
    body = []
    for (scheme, t, policy), cells in sorted(runs.items()):
        p99 = across([c["t99"] for c in cells])
        body.append([
            scheme, t, policy, len(cells), sum(c["n"] for c in cells), sum(c["ok"] for c in cells),
            f"{statistics.mean(c['t50'] for c in cells):.1f}", f"{statistics.mean(c['t95'] for c in cells):.1f}",
            f"{p99['mean']:.1f}", f"{p99['min']:.1f}", f"{p99['max']:.1f}", f"{p99['half']:.1f}",
            f"{statistics.mean(c['d50'] for c in cells):.1f}", f"{statistics.mean(c['d99'] for c in cells):.1f}",
        ])
    print("## handshake: T_hs and D_sign in ms on placement 5/5/20/20/80, signer loss 2%, client RTT 20 ms")
    table(header, body)
    write_csv(out / "handshake.csv", header, body)


def clientloss(stage: Path, out: Path) -> None:
    header = ["server", "delay_ms", "handshakes", "ok", "p50", "p95", "p99", "late150", "late150_pct", "late150_ci95",
              "late900", "late900_pct", "late900_ci95"]
    body = []
    for cell in sorted(p for p in stage.iterdir() if (p / "meta.json").exists()):
        meta = json.loads((cell / "meta.json").read_text())
        measured = [r for r in records(cell / "client.jsonl") if not r["warmup"]]
        ok = [r["t_hs_ms"] for r in measured if r["ok"]]
        median = percentile(ok, 50)
        row = [meta["server"], meta["sign_delay_ms"], len(measured), len(ok), f"{median:.1f}",
               f"{percentile(ok, 95):.1f}", f"{percentile(ok, 99):.1f}"]
        for bound in (150, 900):
            late = sum(1 for v in ok if v - median > bound)
            low, high = wilson(late, len(ok))
            row += [late, f"{100 * late / len(ok):.2f}", f"{100 * low:.2f}-{100 * high:.2f}"]
        body.append(row)
    body.sort(key=lambda r: (r[1], r[0]))
    print("## clientloss: T_hs in ms, client RTT 20 ms, loss 2% per direction")
    table(header, body)
    write_csv(out / "clientloss.csv", header, body)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("run_dir", type=Path)
    args = parser.parse_args()
    if (args.run_dir / "policies").exists():
        policies(args.run_dir / "policies", args.run_dir)
    if (args.run_dir / "newplace").exists():
        newplace(args.run_dir / "newplace", args.run_dir, args.run_dir / "predictions-newplace.csv")
    if (args.run_dir / "handshake").exists():
        handshake(args.run_dir / "handshake", args.run_dir)
    if (args.run_dir / "clientloss").exists():
        clientloss(args.run_dir / "clientloss", args.run_dir)


if __name__ == "__main__":
    main()
