#!/usr/bin/env bash
# Measurement batch 3 of 2026-11-18: threshold ECDSA.
#
# usage: batch3.sh RESULTS_ROOT
# Run from the repository root. Cells that already have a meta.json are
# skipped. With precomputation a coordinator holds 256 presignatures and
# starts refilling at half, so every cell stays below 128 signatures to keep
# presigning out of the measured interval.
set -u
ROOT=${1:?results root}
D="$ROOT/2026-11-18-ecdsa"
sign() { python3 harness/run_sign.py "$@"; }

echo "== stage ecdsa-bench $(date +%T)"
sign bench --out "$D/bench" --configs ecdsa:2:3,ecdsa:3:5,ecdsa:2:3:pre,ecdsa:3:5:pre --policies fastest \
  --transports quic4 --rtts 0,5,50 --n 100
sign bench --out "$D/bench-loss" --configs ecdsa:2:3,ecdsa:2:3:pre --policies fastest \
  --transports quic4,persistent --rtts 5 --losses 2 --n 100

echo "== stage ecdsa-handshake $(date +%T)"
for server in quic-go quic-go-coordd aioquic quiche s2n-quic; do
  sign handshake --out "$D/hs_${server}_conc1" --server $server --configs ecdsa:2:3:pre,ecdsa:3:5:pre \
    --policies fastest --transports quic4 --rtts 5,50 --n 100 --conc 1
  sign handshake --out "$D/hs_${server}_conc10" --server $server --configs ecdsa:2:3:pre \
    --policies fastest --transports quic4 --rtts 5 --n 100 --conc 10
done
sign handshake --out "$D/hs_quic-go_conc1" --server quic-go --configs ecdsa:2:3 \
  --policies fastest --transports quic4 --rtts 5,50 --n 50 --conc 1

echo "== batch3 finished $(date +%T)"
