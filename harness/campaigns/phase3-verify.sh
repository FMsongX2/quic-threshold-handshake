#!/usr/bin/env bash
# Phase 3 verification: the configurations on the measured frontier, and the
# fastest policy beside them, as real handshakes against the quic-go server.
# The signing-delay measurements of phase3.sh ran without a QUIC handshake;
# this checks that their order and size carry over to T_hs.
#
# usage: phase3-verify.sh RESULTS_ROOT
set -u
ROOT=${1:?results root}
D="$ROOT/2026-11-18-phase3/handshake"
sign() { python3 harness/run_sign.py "$@"; }

echo "== stage verify $(date +%T)"
# Region-safe placement (2, 2, 1 signers per region).
sign handshake --out "$D" --configs frost:2:5:pre --policies fastest,hedge4 --transports quic4 \
  --rtts 5/5/20/20/80 --losses 2 --n 1000
sign handshake --out "$D" --configs frost:3:5:pre --policies fastest,fastestn1.5,hedge4,hedge4d1.5 --transports quic4 \
  --rtts 5/5/20/20/80 --losses 2 --n 1000
sign handshake --out "$D" --configs frost:4:5:pre --policies fastest,fastestn1.5 --transports quic4 \
  --rtts 5/5/20/20/80 --losses 2 --n 1000
sign handshake --out "$D" --configs rsa:3:5 --policies fastest,firstn1.5 --transports quic4 \
  --rtts 5/5/20/20/80 --losses 2 --n 1000
# Three signers in the nearest region.
sign handshake --out "$D" --configs frost:2:5:pre --policies hedge4 --transports quic4 \
  --rtts 5/5/5/20/80 --losses 2 --n 1000
sign handshake --out "$D" --configs frost:3:5:pre --policies hedge5 --transports quic4 \
  --rtts 5/5/5/20/80 --losses 2 --n 1000
echo "== verify finished $(date +%T)"
