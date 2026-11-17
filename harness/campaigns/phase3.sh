#!/usr/bin/env bash
# Phase 3 measurement: five signers spread over three regions with signer
# RTTs of 5, 20 and 80 ms. Every policy the delay model ranks is measured for
# every placement and threshold, so the measured order can be compared with
# the predictions written by `model.py grid` before this script ran.
#
# usage: phase3.sh RESULTS_ROOT
set -u
ROOT=${1:?results root}
D="$ROOT/2026-11-18-phase3"
sign() { python3 harness/run_sign.py "$@"; }

FROST_T2=fastest,fastestn1.5,hedge3,hedge4,hedge5,hedge3d1.5,hedge4d1.5,hedge5d1.5
FROST_T3=fastest,fastestn1.5,hedge4,hedge5,hedge4d1.5,hedge5d1.5
FROST_T4=fastest,fastestn1.5,hedge5,hedge5d1.5
PLACEMENTS=5/5/5/20/80,5/5/20/20/80,5/5/20/80/80,5/20/20/80/80,5/20/80/80/80

echo "== stage frost-pre quic $(date +%T)"
sign bench --out "$D/quic" --configs frost:2:5:pre --policies $FROST_T2 --transports quic4 --rtts $PLACEMENTS --losses 0,2 --n 1000
sign bench --out "$D/quic" --configs frost:3:5:pre --policies $FROST_T3 --transports quic4 --rtts $PLACEMENTS --losses 0,2 --n 1000
sign bench --out "$D/quic" --configs frost:4:5:pre --policies $FROST_T4 --transports quic4 --rtts $PLACEMENTS --losses 0,2 --n 1000

echo "== stage frost-pre tcp $(date +%T)"
sign bench --out "$D/tcp" --configs frost:2:5:pre --policies $FROST_T2 --transports persistent --rtts 5/5/20/20/80 --losses 0,2 --n 1000
sign bench --out "$D/tcp" --configs frost:3:5:pre --policies $FROST_T3 --transports persistent --rtts 5/5/20/20/80 --losses 0,2 --n 1000
sign bench --out "$D/tcp" --configs frost:4:5:pre --policies $FROST_T4 --transports persistent --rtts 5/5/20/20/80 --losses 0,2 --n 1000

echo "== stage rsa $(date +%T)"
sign bench --out "$D/quic" --configs rsa:2:5,rsa:3:5 --policies fastest,fastestn1.5,first,firstn1.5 --transports quic4 \
  --rtts 5/5/20/20/80,5/20/20/80/80 --losses 0,2 --n 1000
sign bench --out "$D/tcp" --configs rsa:2:5,rsa:3:5 --policies fastest,fastestn1.5,first,firstn1.5 --transports persistent \
  --rtts 5/5/20/20/80 --losses 0,2 --n 1000

echo "== phase3 finished $(date +%T)"
