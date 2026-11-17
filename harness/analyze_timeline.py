#!/usr/bin/env python3
"""Summarizes fixed-rate handshake cells before and after their first signer event.

For every cell of a run directory measured with run_sign.py --rate and
--events, the handshakes are grouped by start time relative to the first
event: before it, the first five seconds after it, the next five, and the
rest. Each group reports how many handshakes started, how many completed,
and the median and 95th percentile of the completed ones. A handshake that
does not complete counts with the time it took to fail.
"""

import argparse
import csv
import json
from pathlib import Path

from analyze import percentile, read_lines

WINDOWS = [("before", float("-inf"), 0.0), ("0-5s", 0.0, 5.0), ("5-10s", 5.0, 10.0), ("after10s", 10.0, float("inf"))]


def summarize(cell: Path) -> dict | None:
    meta = json.loads((cell / "meta.json").read_text())
    if not meta.get("events") or not meta.get("rate"):
        return None
    event_at = min(float(item.split(":")[0]) for item in meta["events"].split(";") if item)
    records = [json.loads(line) for line in read_lines(cell / "client.jsonl")]
    measured = [r for r in records if not r["warmup"]]
    first_start = min(r["start_unix_ns"] for r in measured)
    row = {
        "scheme": meta["scheme"] + ("-pre" if meta["precompute"] else ""),
        "t": meta["t"], "n": meta["n"], "policy": meta["policy"], "transport": meta["transport"],
        "events": meta["events"], "rate": meta["rate"],
    }
    for name, low, high in WINDOWS:
        group = [r for r in measured if low <= (r["start_unix_ns"] - first_start) / 1e9 - event_at < high]
        ok = [r["t_hs_ms"] for r in group if r["ok"]]
        failed = [r["t_hs_ms"] for r in group if not r["ok"]]
        row[f"{name}_started"] = len(group)
        row[f"{name}_ok"] = len(ok)
        row[f"{name}_t_hs_p50"] = round(percentile(ok, 50), 1) if ok else ""
        row[f"{name}_t_hs_p95"] = round(percentile(ok, 95), 1) if ok else ""
        row[f"{name}_fail_after_p50"] = round(percentile(failed, 50), 1) if failed else ""
    return row


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("run_dir", type=Path)
    args = parser.parse_args()

    rows = [row for cell in sorted(args.run_dir.iterdir()) if (cell / "meta.json").exists() and (row := summarize(cell))]
    if not rows:
        raise SystemExit(f"{args.run_dir} has no fixed-rate cell with events")
    with (args.run_dir / "timeline.csv").open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    print("| scheme | policy | transport | events | " + " | ".join(f"{name}: ok/started, p50, p95" for name, _, _ in WINDOWS) + " |")
    print("|" + " --- |" * (4 + len(WINDOWS)))
    for row in rows:
        cells = [
            f"{row[f'{name}_ok']}/{row[f'{name}_started']}, {row[f'{name}_t_hs_p50']}, {row[f'{name}_t_hs_p95']}"
            for name, _, _ in WINDOWS
        ]
        print(f"| {row['scheme']} | {row['policy']} | {row['transport']} | {row['events']} | " + " | ".join(cells) + " |")


if __name__ == "__main__":
    main()
