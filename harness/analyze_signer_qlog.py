#!/usr/bin/env python3
"""Shows where sign requests wait on the coordinator's QUIC connections to one signer.

A handshake cell measured with run_sign.py --signer-qlog holds the qlog of
every QUIC connection from the coordinator to the signers. For every wave of
concurrent handshakes this script compares three sets of times, all relative
to the start of the wave:

  started   when the coordinator began each signature (server trace)
  sent      when each sign request first left in a packet (qlog)
  answered  when each response first arrived (qlog)

The gap between started and sent is time a request spent waiting to be sent.
The congestion window is reported in packets of 1280 bytes and only makes
sense for a cell with one connection per signer.
"""

import argparse
import datetime
import json
from pathlib import Path

from analyze import percentile, read_lines

PACKET = 1280
# A sign request is longer than this; a ping or a commitment request is not.
SIGN_REQUEST_MIN = 50
WAVE_GAP_NS = 20e6


def reference_ns(header: dict) -> int:
    wall = header["trace"]["common_fields"]["reference_time"]["wall_clock_time"]
    seconds, fraction = wall.rstrip("Z").split(".")
    whole = int(datetime.datetime.fromisoformat(seconds + "+00:00").timestamp())
    return whole * 10**9 + int(fraction.ljust(9, "0")[:9])


def load_qlogs(directory: Path, signer: int):
    sent, answered, resent, window, lost = {}, {}, {}, [], []
    for path in sorted(directory.glob(f"signer{signer}_*.sqlog")):
        reference = None
        for raw in path.read_text(errors="replace").splitlines():
            raw = raw.lstrip("\x1e").strip()
            if not raw:
                continue
            try:
                event = json.loads(raw)
            except json.JSONDecodeError:
                continue  # the last record is cut off when the container is removed
            if reference is None:
                reference = reference_ns(event)
                continue
            at = reference + int(event["time"] * 1e6)
            name, data = event["name"], event.get("data", {})
            if name == "transport:packet_sent":
                for frame in data.get("frames", []):
                    if frame["frame_type"] == "stream" and frame.get("offset", 0) == 0 and frame["length"] > SIGN_REQUEST_MIN:
                        key = (path.name, frame["stream_id"])
                        if key in sent:
                            resent[key] = resent.get(key, 0) + 1
                        else:
                            sent[key] = at
            elif name == "transport:packet_received":
                for frame in data.get("frames", []):
                    if frame["frame_type"] == "stream" and frame.get("offset", 0) == 0:
                        answered.setdefault((path.name, frame["stream_id"]), at)
            elif name == "recovery:metrics_updated" and "congestion_window" in data:
                window.append((at, data["congestion_window"]))
            elif name == "recovery:packet_lost":
                lost.append(at)
    window.sort()
    return sent, answered, resent, window, lost


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("cell", type=Path)
    parser.add_argument("signer", type=int)
    args = parser.parse_args()

    traces = sorted((json.loads(line) for line in read_lines(args.cell / "server-trace.jsonl")), key=lambda t: t["start_unix_ns"])
    waves = [[traces[0]]]
    for trace in traces[1:]:
        if trace["start_unix_ns"] - waves[-1][-1]["start_unix_ns"] > WAVE_GAP_NS:
            waves.append([])
        waves[-1].append(trace)
    waves = [wave for wave in waves if len(wave) >= 2]
    sent, answered, resent, window, lost = load_qlogs(args.cell / "signer-qlog", args.signer)

    rows = []
    for i, wave in enumerate(waves):
        start = wave[0]["start_unix_ns"]
        end = waves[i + 1][0]["start_unix_ns"] if i + 1 < len(waves) else start + 400e6
        keys = [key for key, at in sent.items() if start - 2e6 <= at < end]
        if len(keys) < 0.9 * len(wave):
            continue
        started = [(t["start_unix_ns"] - start) / 1e6 for t in wave]
        first_sent = [(sent[key] - start) / 1e6 for key in keys]
        response = [(answered[key] - sent[key]) / 1e6 for key in keys if key in answered]
        before = [cwnd for at, cwnd in window if at < start]
        rows.append({
            "wait_p50": percentile(first_sent, 50) - percentile(started, 50),
            "wait_p90": percentile(first_sent, 90) - percentile(started, 90),
            "response_p50": percentile(response, 50),
            "response_p90": percentile(response, 90),
            "cwnd_packets": before[-1] / PACKET if before else 0,
            # Requests that left at the same instant on one connection shared a packet.
            "request_packets": len({(key[0], sent[key]) for key in keys}),
            "lost_packets": sum(1 for at in lost if start <= at < end),
            "resent_requests": sum(resent.get(key, 0) for key in keys),
        })

    print(f"{args.cell.name} signer {args.signer}: {len(rows)} waves, {len(sent)} sign requests, {len(lost)} packets declared lost")
    print("| per wave | p10 | p50 | p90 |")
    print("| --- | --- | --- | --- |")
    for field in rows[0] if rows else []:
        values = [row[field] for row in rows]
        print(f"| {field} | {percentile(values, 10):.1f} | {percentile(values, 50):.1f} | {percentile(values, 90):.1f} |")


if __name__ == "__main__":
    main()
