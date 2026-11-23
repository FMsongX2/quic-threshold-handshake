#!/usr/bin/env python3
"""Draws the figures of the paper from the measurement records in results/.

usage: figures.py [NAME ...]

Without names every figure is drawn. Each figure is written to
paper/figures/ as a PDF and a PNG. A figure whose input directory is missing
is skipped with a message.
"""

import csv
import gzip
import json
import statistics
import sys
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import FancyArrowPatch, FancyBboxPatch

ROOT = Path(__file__).resolve().parent.parent
RESULTS = ROOT / "results"
OUT = ROOT / "paper" / "figures"
sys.path.insert(0, str(ROOT / "harness"))
from analyze import percentile  # noqa: E402

SERVERS = ["quic-go", "aioquic", "quiche", "s2n-quic"]
COLOR = {"quic-go": "#0072B2", "aioquic": "#D55E00", "quiche": "#009E73", "s2n-quic": "#CC79A7"}
MARKER = {"quic-go": "o", "aioquic": "s", "quiche": "^", "s2n-quic": "D"}

plt.rcParams.update({
    "font.size": 8, "axes.labelsize": 8, "axes.titlesize": 8, "legend.fontsize": 7,
    "xtick.labelsize": 7, "ytick.labelsize": 7, "axes.linewidth": 0.6, "lines.linewidth": 1.0,
    "lines.markersize": 3.5, "axes.grid": True, "grid.linewidth": 0.3, "grid.alpha": 0.6,
    "legend.frameon": False, "pdf.fonttype": 42, "savefig.bbox": "tight", "savefig.pad_inches": 0.02,
})


def rows(path: Path) -> list[dict]:
    with path.open() as f:
        return list(csv.DictReader(f))


def records(path: Path) -> list[dict]:
    """Reads a JSON-lines file, compressed or not."""
    if not path.exists():
        path = path.with_name(path.name + ".gz")
    opener = gzip.open if path.suffix == ".gz" else open
    with opener(path, "rt") as f:
        return [json.loads(line) for line in f if line.strip()]


def save(fig, name: str) -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    fig.savefig(OUT / f"{name}.pdf")
    fig.savefig(OUT / f"{name}.png", dpi=300)
    plt.close(fig)
    print(f"wrote {name}")


def ccdf(values: list[float]) -> tuple[list[float], list[float]]:
    ordered = sorted(values)
    n = len(ordered)
    return ordered, [(n - i) / n for i in range(n)]


def system() -> None:
    """The system model: client, front server with coordinator, signers."""
    fig, ax = plt.subplots(figsize=(3.4, 1.7))
    ax.set_axis_off()
    ax.set_xlim(0, 10)
    ax.set_ylim(0, 4.6)

    def box(x, y, w, h, text, face="#F2F2F2", size=6.3):
        ax.add_patch(FancyBboxPatch((x, y), w, h, boxstyle="round,pad=0.02,rounding_size=0.08",
                                    linewidth=0.6, edgecolor="black", facecolor=face))
        ax.text(x + w / 2, y + h / 2, text, ha="center", va="center", fontsize=size)

    def arrow(start, end):
        ax.add_patch(FancyArrowPatch(start, end, arrowstyle="<->", mutation_scale=6, linewidth=0.6, color="black",
                                     shrinkA=1, shrinkB=1))

    box(0.1, 1.8, 1.5, 1.0, "Client")
    box(3.1, 1.0, 2.7, 2.6, "Front server\n(QUIC, TLS 1.3)", face="#E3EEF7", size=6.0)
    box(3.45, 1.2, 2.0, 0.7, "Coordinator", face="white", size=6.0)
    ax.texts[-2].set_position((4.45, 2.85))
    arrow((1.6, 2.3), (3.1, 2.3))
    ax.text(2.35, 2.42, "$RTT_c$", ha="center", va="bottom", fontsize=6.5)
    ax.text(0.85, 1.35, "$T_{hs}$", ha="center", fontsize=7)
    ax.text(4.45, 0.45, "$D_{sign}$", ha="center", fontsize=7)
    for y, label, name, text_y in [(3.5, "Signer 1", "1", 3.62), (2.05, "Signer 2", "2", 2.6), (0.2, "Signer $n$", "n", 1.25)]:
        box(8.3, y, 1.6, 0.8, label)
        arrow((5.8, 1.55 + (y - 0.2) * 0.3), (8.3, y + 0.4))
        ax.text(7.05, text_y, f"$RTT_{{s,{name}}}$", ha="center", va="bottom", fontsize=6.5)
    ax.text(9.1, 1.3, "...", ha="center", fontsize=8)
    save(fig, "system")


def additivity() -> None:
    """Median handshake time minus the baseline and the injected delay."""
    sources = {
        "quic-go client": [
            RESULTS / "2026-11-13-pilot-sweep", RESULTS / "2026-11-18-p1-rttc", RESULTS / "2026-11-18-p1-s2n" / "sweep",
        ],
        "s2n-quic client": [RESULTS / "2026-11-18-client-s2n" / "sweep"],
    }
    fig, axes = plt.subplots(1, 2, figsize=(7.0, 2.1), sharey=True)
    fills = {1.0: "none", 20.0: "full", 100.0: "left"}
    for ax, (client, dirs) in zip(axes, sources.items()):
        for d in dirs:
            for server in SERVERS:
                for rtt in (1.0, 20.0, 100.0):
                    sel = sorted(
                        (r for r in rows(d / "summary.csv")
                         if r["server"] == server and float(r["rtt_ms"]) == rtt and float(r["concurrency"]) == 1
                         and float(r["loss_pct"]) == 0 and float(r["sign_delay_ms"]) > 0),
                        key=lambda r: float(r["sign_delay_ms"]),
                    )
                    if not sel:
                        continue
                    ax.plot([float(r["sign_delay_ms"]) for r in sel], [float(r["excess_p50"]) for r in sel],
                            marker=MARKER[server], color=COLOR[server], fillstyle=fills[rtt], linewidth=0.5,
                            markeredgewidth=0.6)
        ax.axhspan(-1, 1, color="0.85", zorder=0)
        ax.set_xscale("log")
        ax.set_xlabel("Injected signing delay (ms)")
        ax.set_title(client)
        ax.set_ylim(-2, 2)
    axes[0].set_ylabel("Median $T_{hs}$ $-$ baseline $-$ delay (ms)")
    handles = [plt.Line2D([], [], marker=MARKER[s], color=COLOR[s], linewidth=0.5, label=s) for s in SERVERS]
    handles += [plt.Line2D([], [], marker="o", color="0.3", fillstyle=f, linewidth=0, markeredgewidth=0.6,
                           label=f"$RTT_c$ {int(r)} ms") for r, f in fills.items()]
    axes[1].legend(handles=handles, ncol=2, loc="lower right")
    save(fig, "additivity")


def concurrency() -> None:
    """Handshake time under concurrent handshakes and under a fixed arrival rate."""
    fig, axes = plt.subplots(1, 2, figsize=(7.0, 2.2))
    conc = rows(RESULTS / "2026-11-13-pilot-concurrency" / "summary.csv") + rows(
        RESULTS / "2026-11-18-p1-s2n" / "concurrency" / "summary.csv")
    for server in SERVERS:
        sel = sorted((r for r in conc if r["server"] == server and float(r["sign_delay_ms"]) == 50),
                     key=lambda r: float(r["concurrency"]))
        axes[0].plot([float(r["concurrency"]) for r in sel], [float(r["t_hs_p50"]) for r in sel],
                     marker=MARKER[server], color=COLOR[server], label=server)
    axes[0].set_xscale("log")
    axes[0].set_yscale("log")
    axes[0].set_xlabel("Concurrent handshakes")
    axes[0].set_ylabel("Median $T_{hs}$ (ms)")
    axes[0].set_title("(a) Injected delay 50 ms")
    axes[0].legend()

    rate = rows(RESULTS / "2026-11-18-p1-rate" / "summary.csv") + rows(RESULTS / "2026-11-18-p1-s2n" / "rate" / "summary.csv")
    for server in SERVERS:
        sel = sorted((r for r in rate if r["server"] == server and float(r["sign_delay_ms"]) == 20),
                     key=lambda r: float(r["rate"]))
        axes[1].plot([float(r["rate"]) for r in sel], [float(r["completed_per_s"]) for r in sel],
                     marker=MARKER[server], color=COLOR[server], label=server)
    axes[1].plot([50, 1600], [50, 1600], color="0.5", linewidth=0.5, linestyle="--", zorder=0)
    axes[1].set_xscale("log")
    axes[1].set_yscale("log")
    axes[1].set_xlabel("Offered handshakes per second")
    axes[1].set_ylabel("Completed handshakes per second")
    axes[1].set_title("(b) Injected delay 20 ms")
    save(fig, "concurrency")


def clientloss() -> None:
    """Complementary CDF of the handshake time above its median under client-link loss."""
    base = RESULTS / "2026-11-23-repeat" / "clientloss"
    if not base.exists():
        print("skipped clientloss: no repeat data")
        return
    delays = [0, 20, 100]
    fig, axes = plt.subplots(1, 3, figsize=(7.0, 2.1), sharey=True)
    for ax, delay in zip(axes, delays):
        for server in SERVERS:
            cell = base / f"{server}_rtt20_loss2_d{delay}_c1"
            times = [r["t_hs_ms"] for r in records(cell / "client.jsonl") if not r["warmup"] and r["ok"]]
            median = statistics.median(times)
            x, y = ccdf([t - median for t in times])
            ax.step(x, y, where="post", color=COLOR[server], label=server)
        ax.set_xscale("log")
        ax.set_yscale("log")
        ax.set_xlim(1, 3000)
        ax.set_ylim(4e-4, 0.3)
        ax.set_xlabel("$T_{hs}$ above its median (ms)")
        ax.set_title(f"Injected delay {delay} ms")
    axes[0].set_ylabel("Fraction of handshakes")
    axes[0].legend(loc="lower left")
    save(fig, "clientloss")


POLICY_LABEL = {
    "fastest": "fastest", "fastestn1.5": "fastest+nudge", "hedge3": "hedge (3)", "hedge4": "hedge (4)",
    "hedge5": "hedge (5)", "hedge3d1.5": "delayed hedge (3)", "hedge4d1.5": "delayed hedge (4)",
    "hedge5d1.5": "delayed hedge (5)", "first": "first", "firstn1.5": "first+nudge",
}
POLICY_STYLE = {
    "fastest": ("#D55E00", "-"), "fastestn1.5": ("#E69F00", "--"), "hedge4": ("#0072B2", "-"),
    "hedge4d1.5": ("#56B4E9", "--"), "hedge5": ("#009E73", "-."),
}


def bench_cells(base: Path, scheme: str, t: int, policy: str, placement: str) -> list[list[float]]:
    """D_sign in ms of every repetition of one bench cell."""
    name = f"{scheme}_t{t}n5_{policy}_quic4_rtt{placement.replace('/', '-')}_loss2"
    out = []
    for rep in sorted(base.glob("rep*")):
        cell = rep / name
        if (cell / "meta.json").exists():
            out.append([r["total_ns"] / 1e6 for r in records(cell / "sign.jsonl")])
    return out


def policies() -> None:
    """Signing delay of the selection policies on the region-safe placement."""
    base = RESULTS / "2026-11-23-repeat" / "policies"
    if not base.exists():
        print("skipped policies: no repeat data")
        return
    placement = "5/5/20/20/80"
    fig, axes = plt.subplots(1, 2, figsize=(7.0, 2.3), gridspec_kw={"width_ratios": [1.0, 1.25]})
    for policy, (color, line) in POLICY_STYLE.items():
        reps = bench_cells(base, "frost-pre", 3, policy, placement)
        if not reps:
            continue
        x, y = ccdf([v for rep in reps for v in rep])
        axes[0].step(x, y, where="post", color=color, linestyle=line, label=POLICY_LABEL[policy])
    axes[0].set_yscale("log")
    axes[0].set_xlim(15, 130)
    axes[0].set_ylim(5e-4, 1)
    axes[0].axhline(0.01, color="0.5", linewidth=0.5, linestyle=":")
    axes[0].set_xlabel("$D_{sign}$ (ms)")
    axes[0].set_ylabel("Fraction of signatures above")
    axes[0].set_title("(a) FROST, $t=3$, five runs pooled")
    axes[0].legend()

    groups = [
        ("frost-pre", 2, ["fastest", "fastestn1.5", "hedge3", "hedge4", "hedge3d1.5"]),
        ("frost-pre", 3, ["fastest", "fastestn1.5", "hedge4", "hedge5", "hedge4d1.5"]),
        ("frost-pre", 4, ["fastest", "fastestn1.5", "hedge5", "hedge5d1.5"]),
        ("rsa", 3, ["fastest", "fastestn1.5", "first", "firstn1.5"]),
    ]
    position, ticks, labels, bounds = 0, [], [], []
    for scheme, t, names in groups:
        start = position
        for policy in names:
            p99 = [percentile(rep, 99) for rep in bench_cells(base, scheme, t, policy, placement)]
            if not p99:
                continue
            axes[1].plot([position] * len(p99), p99, linestyle="", marker="o", markersize=2.5, color="0.35",
                         fillstyle="none", markeredgewidth=0.5)
            axes[1].plot([position - 0.3, position + 0.3], [statistics.mean(p99)] * 2, color="black", linewidth=1.0)
            ticks.append(position)
            labels.append(POLICY_LABEL[policy])
            position += 1
        bounds.append((start, position - 1, f"{'FROST' if scheme == 'frost-pre' else 'RSA'} $t={t}$"))
        position += 0.6
    for start, end, text in bounds:
        axes[1].text((start + end) / 2, 1.02, text, transform=axes[1].get_xaxis_transform(), ha="center", fontsize=7)
    axes[1].set_xticks(ticks)
    axes[1].set_xticklabels(labels, rotation=60, ha="right")
    axes[1].set_ylabel("p99 of $D_{sign}$ per run (ms)")
    axes[1].set_ylim(0, None)
    axes[1].set_title("(b) Five runs per policy; bar: mean", pad=12)
    save(fig, "policies")


def events() -> None:
    """Handshake time around the moment one signer stops answering."""
    base = RESULTS / "2026-11-18-signer-events" / "quic4_stop"
    panels = [
        ("fastest", "fastest"), ("hedge", "hedge"), ("hedged1.5", "delayed hedge"),
        ("hedge4d1.5b0.2", "delayed hedge, budget 0.2"),
    ]
    fig, axes = plt.subplots(1, 4, figsize=(7.0, 1.8), sharey=True)
    for ax, (policy, title) in zip(axes, panels):
        cell = base / f"frost-pre_t3n5_{policy}_quic4_rtt50-5-100-1-20_loss0"
        measured = [r for r in records(cell / "client.jsonl") if not r["warmup"]]
        start = min(r["start_unix_ns"] for r in measured)
        ok = [((r["start_unix_ns"] - start) / 1e9, r["t_hs_ms"]) for r in measured if r["ok"]]
        failed = [(r["start_unix_ns"] - start) / 1e9 for r in measured if not r["ok"]]
        ax.plot([p[0] for p in ok], [p[1] for p in ok], linestyle="", marker=".", markersize=1.5, color="#0072B2")
        ax.plot(failed, [260] * len(failed), linestyle="", marker="x", markersize=2, color="#D55E00", markeredgewidth=0.4)
        ax.axvline(15, color="0.4", linewidth=0.5, linestyle="--")
        ax.set_title(title)
        ax.set_xlabel("Handshake start (s)")
        ax.set_ylim(0, 280)
        ax.set_xlim(0, 40)
    axes[0].set_ylabel("$T_{hs}$ (ms)")
    axes[0].text(16, 235, "failed", color="#D55E00", fontsize=6.5)
    save(fig, "events")


FIGURES = {
    "system": system, "additivity": additivity, "concurrency": concurrency, "clientloss": clientloss,
    "policies": policies, "events": events,
}


def main() -> None:
    names = sys.argv[1:] or list(FIGURES)
    for name in names:
        FIGURES[name]()


if __name__ == "__main__":
    main()
