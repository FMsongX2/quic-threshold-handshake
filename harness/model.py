#!/usr/bin/env python3
"""Delay model of a threshold signature made one at a time over kept connections.

The model draws the response time of every signer, combines the draws the way
the signer selection policy does, and reports quantiles of the signing delay
D_sign. A response takes the signer's round trip r plus its compute time. A
request or response lost on a connection with no other traffic is repaired by
a timer of the transport, which makes the response take 2r + c with c fixed by
the transport, or by the ping of the nudge policy.

  model.py predict  --placement 5/5/20/20/80 --t 3 --scheme frost-pre --loss 2
  model.py grid     --placements 5/5/20/20/80,5/20/20/80/80 --ts 2,3 --losses 0,2 --out FILE
  model.py validate RUN_DIR [RUN_DIR ...]

predict ranks every policy and transport the model knows for one placement.
grid writes the predictions for a set of placements and thresholds as CSV,
with two properties of the placement next to them: how many whole regions
(signers with the same RTT) an attacker must take over to hold t shares, and
whether t signers remain when the largest region is lost. validate compares
the model with the summary.csv of measured run directories and skips rows
outside the model: per-request transports, runs with several handshakes in
flight, and runs whose signers change.

Constants come from the measurements in docs/results-phase2-signer.md and
docs/results-phase3-signer-transport.md.
"""

import argparse
import csv
import itertools
import random
import re
from pathlib import Path

# Milliseconds added to 2r when a loss is repaired by the transport's timer.
TIMER_MS = {"tcp": 210.0, "quic": 27.0}
# Signer compute time on the critical path per round, coordinator-local time
# per signature, and request rounds per signature.
SCHEMES = {
    "plain-ed25519": {"rounds": 1, "compute": 0.02, "local": 0.03},
    "plain-rsa": {"rounds": 1, "compute": 0.55, "local": 0.02},
    "frost-pre": {"rounds": 1, "compute": 0.13, "local": 0.14},
    "frost": {"rounds": 2, "compute": 0.11, "local": 0.15},
    "rsa": {"rounds": 1, "compute": 4.4, "local": 0.17},
}
# What a round trip costs beyond the configured link delay.
LINK_OVERHEAD_MS = 0.07
# The nudge and hedge delays add this slack to their multiple of the RTT.
STALL_SLACK_MS = 2.0
HEDGE = re.compile(r"hedge(\d*)(?:d([\d.]+))?(?:b([\d.]+))?")
NUDGE = re.compile(r"(fixed|fastest|first)(?:n([\d.]+))?")
POOLED = re.compile(r"(persistent|tls|quic)(\d*)")


def response_ms(rng: random.Random, rtt: float, compute: float, loss: float, timer: float, nudge_at: float) -> float:
    """One draw of the time from sending a request to having its response.
    loss is the per-direction loss probability; nudge_at is the time after
    which the policy pings a silent signer, or 0 without nudging."""
    normal = rtt + LINK_OVERHEAD_MS + compute
    if rng.random() >= 1 - (1 - loss) ** 2:
        return normal
    repaired = 2 * rtt + timer + compute
    # A ping sent before the transport's timer fires re-arms that timer, so
    # the ping decides; one sent later changes nothing.
    if nudge_at and nudge_at < rtt + timer:
        repaired = nudge_at + 2 * rtt + compute
    return repaired


def kth(values: list[float], k: int) -> float:
    return sorted(values)[k - 1]


def sample_d_sign(rng: random.Random, scheme: str, t: int, rtts: list[float], policy: str, transport: str, loss: float) -> float:
    """One draw of D_sign. rtts are listed in signer ID order."""
    spec = SCHEMES[scheme]
    timer = TIMER_MS["quic" if transport.startswith("quic") else "tcp"]
    nearest = sorted(range(len(rtts)), key=lambda i: rtts[i])

    def draw(i: int, nudge_at: float = 0.0) -> float:
        return response_ms(rng, rtts[i], spec["compute"], loss, timer, nudge_at)

    if hedge := HEDGE.fullmatch(policy):
        width = int(hedge[1]) if hedge[1] else len(rtts)
        members = nearest[:width]
        if hedge[2] is None:
            # Every t-subset runs its own session at once. A session needs all
            # of its signers in every round, so the signature takes as long as
            # the subset whose slowest signers, round by round, are fastest.
            rounds = [{i: draw(i) for i in members} for _ in range(spec["rounds"])]
            return spec["local"] + min(
                sum(max(draws[i] for i in subset) for draws in rounds)
                for subset in itertools.combinations(members, t)
            )
        # The t nearest signers start alone; the other subsets start only if
        # those have not finished after the delay. A signer stalled in the
        # first session answers the later ones with a fresh request, but the
        # first session itself still waits for its repair. One request round
        # only.
        primary = max(draw(i) for i in members[:t])
        widen_at = float(hedge[2]) * (max(rtts[i] for i in members[:t]) + spec["compute"]) + STALL_SLACK_MS
        if primary <= widen_at:
            return spec["local"] + primary
        late = {i: draw(i) for i in members}
        others = [max(late[i] for i in subset) for subset in itertools.combinations(members, t) if subset != tuple(members[:t])]
        return spec["local"] + min(primary, widen_at + min(others))

    nudge = NUDGE.fullmatch(policy)
    if nudge[1] == "first":
        members = list(range(len(rtts)))
    elif nudge[1] == "fastest":
        members = nearest[:t]
    else:
        members = list(range(t))
    nudge_at = float(nudge[2]) * max(rtts[i] for i in members) + STALL_SLACK_MS if nudge[2] else 0.0
    return spec["local"] + sum(kth([draw(i, nudge_at) for i in members], t) for _ in range(spec["rounds"]))


def quantiles(scheme: str, t: int, rtts: list[float], policy: str, transport: str, loss_pct: float,
              samples: int = 200_000, seed: int = 1) -> dict:
    rng = random.Random(seed)
    draws = sorted(sample_d_sign(rng, scheme, t, rtts, policy, transport, loss_pct / 100) for _ in range(samples))
    return {name: draws[min(samples - 1, int(q * samples))] for name, q in (("p50", 0.50), ("p95", 0.95), ("p99", 0.99))}


def candidate_policies(scheme: str, t: int, n: int) -> list[str]:
    if scheme.startswith("plain"):
        return ["fixed"]
    policies = ["fastest", "fastestn1.5"]
    if scheme == "rsa":
        policies += ["first", "firstn1.5"]
    else:
        policies += [f"hedge{m}" for m in range(t + 1, n + 1)] + [f"hedge{m}d1.5" for m in range(t + 1, n + 1)]
    return policies


def predict(args) -> None:
    rtts = [float(v) for v in args.placement.split("/")]
    rows = []
    for policy in candidate_policies(args.scheme, args.t, len(rtts)):
        for transport in ("persistent", "quic"):
            q = quantiles(args.scheme, args.t, rtts, policy, transport, args.loss)
            rows.append((q["p99"], q["p95"], q["p50"], policy, transport))
    print("| policy | transport | D_sign p50 | p95 | p99 |")
    print("| --- | --- | --- | --- | --- |")
    for p99, p95, p50, policy, transport in sorted(rows):
        print(f"| {policy} | {transport} | {p50:.1f} | {p95:.1f} | {p99:.1f} |")


def grid(args) -> None:
    fields = ["scheme", "t", "n", "placement", "regions_to_hold_t", "survives_largest_region", "loss_pct",
              "policy", "transport", "d_sign_p50", "d_sign_p95", "d_sign_p99"]
    with args.out.open("w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=fields)
        writer.writeheader()
        for placement, t, scheme, loss in itertools.product(
            args.placements.split(","), [int(v) for v in args.ts.split(",")], args.schemes.split(","),
            [float(v) for v in args.losses.split(",")],
        ):
            rtts = [float(v) for v in placement.split("/")]
            sizes = sorted((rtts.count(r) for r in set(rtts)), reverse=True)
            regions = next(k for k in range(1, len(sizes) + 1) if sum(sizes[:k]) >= t)
            for policy, transport in itertools.product(candidate_policies(scheme, t, len(rtts)), ("quic", "persistent")):
                q = quantiles(scheme, t, rtts, policy, transport, loss, samples=100_000)
                writer.writerow({
                    "scheme": scheme, "t": t, "n": len(rtts), "placement": placement, "regions_to_hold_t": regions,
                    "survives_largest_region": len(rtts) - sizes[0] >= t, "loss_pct": loss, "policy": policy,
                    "transport": transport, **{f"d_sign_{name}": round(value, 2) for name, value in q.items()},
                })


def modelled(row: dict) -> bool:
    hedge = HEDGE.fullmatch(row["policy"])
    return (
        row["scheme"] in SCHEMES
        and POOLED.fullmatch(row["transport"]) is not None
        and (hedge or NUDGE.fullmatch(row["policy"])) is not None
        # A hedge budget and a delayed hedge over two rounds are not modelled.
        and not (hedge and (hedge[3] or (hedge[2] and SCHEMES[row["scheme"]]["rounds"] > 1)))
        and float(row.get("concurrency") or 1) == 1
        and not float(row.get("rate") or 0)
        and not row.get("events")
        and not float(row.get("signer_jitter_pct") or 0)
        and row.get("d_sign_p50") not in ("", None)
    )


def validate(args) -> None:
    rows = []
    for run_dir in args.run_dirs:
        for row in csv.DictReader((run_dir / "summary.csv").open()):
            if modelled(row):
                row["run"] = run_dir.name
                rows.append(row)
    print("| run | scheme | t | policy | transport | RTT_s | loss | p50 model / measured | p95 | p99 |")
    print("|" + " --- |" * 10)
    errors = {"p50": [], "p95": [], "p99": []}
    for row in rows:
        rtts = [float(v) for v in row["signer_rtts_ms"].split("/")]
        q = quantiles(row["scheme"], int(row["t"]), rtts, row["policy"], row["transport"], float(row["signer_loss_pct"]), samples=50_000)
        cells = []
        for name in ("p50", "p95", "p99"):
            measured = float(row[f"d_sign_{name}"])
            errors[name].append(abs(q[name] - measured))
            cells.append(f"{q[name]:.1f} / {measured:.1f}")
        print(f"| {row['run']} | {row['scheme']} | {row['t']} | {row['policy']} | {row['transport']} | {row['signer_rtts_ms']} "
              f"| {row['signer_loss_pct']} | " + " | ".join(cells) + " |")
    for name, values in errors.items():
        ordered = sorted(values)
        print(f"{name}: {len(values)} rows, median absolute error {ordered[len(ordered) // 2]:.2f} ms, "
              f"90th percentile {ordered[int(len(ordered) * 0.9)]:.2f} ms, largest {ordered[-1]:.2f} ms")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    p = commands.add_parser("predict")
    p.add_argument("--placement", required=True, help="signer RTTs in ms in ID order, a/b/c")
    p.add_argument("--t", type=int, required=True)
    p.add_argument("--scheme", default="frost-pre", choices=sorted(SCHEMES))
    p.add_argument("--loss", type=float, default=0, help="per-direction loss on signer links in percent")
    p.set_defaults(run=predict)
    g = commands.add_parser("grid")
    g.add_argument("--placements", required=True, help="comma-separated placements, each a/b/c in signer ID order")
    g.add_argument("--ts", required=True, help="comma-separated thresholds")
    g.add_argument("--schemes", default="frost-pre")
    g.add_argument("--losses", default="0,2")
    g.add_argument("--out", required=True, type=Path)
    g.set_defaults(run=grid)
    v = commands.add_parser("validate")
    v.add_argument("run_dirs", nargs="+", type=Path)
    v.set_defaults(run=validate)
    args = parser.parse_args()
    args.run(args)


if __name__ == "__main__":
    main()
