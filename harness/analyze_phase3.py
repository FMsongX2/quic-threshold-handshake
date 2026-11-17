#!/usr/bin/env python3
"""Compares the delay model's predictions with the Phase 3 measurements.

usage: analyze_phase3.py OUT_DIR PREDICTIONS.csv [PREDICTIONS.csv ...] -- RUN_DIR [RUN_DIR ...]

A group is one scheme, placement, threshold, loss rate and transport. For
every group with loss the script reports whether the policy with the lowest
measured p99 of D_sign is among the three policies the model ranks best
(policies whose predicted p99 lie within TIE_MS of each other share a rank),
how much worse the model's first choice measured than the measured best, and
the rank correlation between predicted and measured p99.

It also writes one row per placement and threshold with the two properties of
the placement (regions an attacker must take over to hold t shares, and
whether t signers remain when the largest region is lost) next to the lowest
measured median and p99 over the policies.
"""

import argparse
import csv
import sys
from collections import defaultdict
from pathlib import Path

TIE_MS = 0.5


def family(transport: str) -> str:
    return "quic" if transport.startswith("quic") else "persistent"


def ranks(values: list[float]) -> list[int]:
    """Dense ranks, starting at 1, where values within TIE_MS of the previous
    distinct value share its rank."""
    order = sorted(range(len(values)), key=lambda i: values[i])
    out = [0] * len(values)
    rank, anchor = 0, None
    for i in order:
        if anchor is None or values[i] - anchor > TIE_MS:
            rank, anchor = rank + 1, values[i]
        out[i] = rank
    return out


def spearman(a: list[float], b: list[float]) -> float | None:
    def average_ranks(values: list[float]) -> list[float]:
        order = sorted(range(len(values)), key=lambda i: values[i])
        out = [0.0] * len(values)
        i = 0
        while i < len(order):
            j = i
            while j + 1 < len(order) and values[order[j + 1]] == values[order[i]]:
                j += 1
            for k in range(i, j + 1):
                out[order[k]] = (i + j) / 2 + 1
            i = j + 1
        return out

    ra, rb = average_ranks(a), average_ranks(b)
    mean_a, mean_b = sum(ra) / len(ra), sum(rb) / len(rb)
    cov = sum((x - mean_a) * (y - mean_b) for x, y in zip(ra, rb))
    var_a, var_b = sum((x - mean_a) ** 2 for x in ra), sum((y - mean_b) ** 2 for y in rb)
    return cov / (var_a * var_b) ** 0.5 if var_a and var_b else None


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("out_dir", type=Path)
    parser.add_argument("paths", nargs="+", type=Path, help="prediction files, then --, then run directories")
    argv = sys.argv[1:]
    split = argv.index("--")
    args = parser.parse_args(argv[:split])
    run_dirs = [Path(p) for p in argv[split + 1:]]

    predicted, placement_facts = {}, {}
    for path in args.paths:
        for row in csv.DictReader(path.open()):
            key = (row["scheme"], row["placement"], int(row["t"]), float(row["loss_pct"]), row["transport"], row["policy"])
            predicted[key] = row
            placement_facts[(row["placement"], int(row["t"]))] = (row["regions_to_hold_t"], row["survives_largest_region"])

    groups = defaultdict(list)
    for run_dir in run_dirs:
        for row in csv.DictReader((run_dir / "summary.csv").open()):
            key = (row["scheme"], row["signer_rtts_ms"], int(row["t"]), float(row["signer_loss_pct"]),
                   family(row["transport"]), row["policy"])
            if key in predicted and row["d_sign_p99"]:
                groups[key[:5]].append((row["policy"], predicted[key], row))

    comparison, hits = [], []
    for group, members in sorted(groups.items()):
        scheme, placement, t, loss, transport = group
        model = [float(p["d_sign_p99"]) for _, p, _ in members]
        measured = [float(m["d_sign_p99"]) for _, _, m in members]
        model_rank = ranks(model)
        best = min(range(len(members)), key=lambda i: measured[i])
        choice = min(range(len(members)), key=lambda i: model[i])
        rho = spearman(model, measured)
        row = {
            "scheme": scheme, "placement": placement, "t": t, "loss_pct": loss, "transport": transport,
            "policies": len(members),
            "measured_best": members[best][0], "measured_best_p99": measured[best],
            "model_choice": members[choice][0], "model_choice_measured_p99": measured[choice],
            "regret_ms": round(measured[choice] - measured[best], 2),
            "best_in_model_top3": model_rank[best] <= 3,
            "spearman_p99": round(rho, 2) if rho is not None else "",
        }
        comparison.append(row)
        if loss > 0:
            hits.append(row["best_in_model_top3"])

    args.out_dir.mkdir(parents=True, exist_ok=True)
    with (args.out_dir / "model-vs-measured.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=list(comparison[0]))
        writer.writeheader()
        writer.writerows(comparison)

    frontier = []
    for (scheme, placement, t, loss, transport), members in sorted(groups.items()):
        best_p99 = min(members, key=lambda m: float(m[2]["d_sign_p99"]))
        best_p50 = min(members, key=lambda m: float(m[2]["d_sign_p50"]))
        regions, survives = placement_facts[(placement, t)]
        frontier.append({
            "scheme": scheme, "placement": placement, "t": t, "loss_pct": loss, "transport": transport,
            "regions_to_hold_t": regions, "survives_largest_region": survives,
            "best_p50_ms": best_p50[2]["d_sign_p50"], "best_p50_policy": best_p50[0],
            "best_p99_ms": best_p99[2]["d_sign_p99"], "best_p99_policy": best_p99[0],
        })
    with (args.out_dir / "frontier.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=list(frontier[0]))
        writer.writeheader()
        writer.writerows(frontier)

    print("| scheme | placement | t | loss | transport | measured best (p99) | model choice (measured p99) | regret | best in model top 3 | rank correlation |")
    print("|" + " --- |" * 10)
    for row in comparison:
        print(f"| {row['scheme']} | {row['placement']} | {row['t']} | {row['loss_pct']:g} | {row['transport']} "
              f"| {row['measured_best']} ({row['measured_best_p99']:.1f}) "
              f"| {row['model_choice']} ({row['model_choice_measured_p99']:.1f}) | {row['regret_ms']} "
              f"| {row['best_in_model_top3']} | {row['spearman_p99']} |")
    if hits:
        print(f"groups with loss: {len(hits)}, measured best within the model's top three: {sum(hits)}")


if __name__ == "__main__":
    main()
