#!/usr/bin/env bash
# Repeated measurements of 2026-11-23 for the claims about tails.
#
# The earlier campaigns measured every condition once, so a 99th percentile
# rested on about ten samples of one run. This campaign repeats the
# conditions the conclusions depend on, enlarges the client-loss cells, and
# measures two placements that were not used to derive the spare-signer rule.
#
# usage: repeat.sh RESULTS_ROOT [STAGE ...]
# Run from the repository root. Cells that already have a meta.json are
# skipped, so the script can be restarted.
set -u
ROOT=${1:?results root}
shift
STAGES=${*:-policies newplace handshake clientloss}
D="$ROOT/2026-11-23-repeat"
REPS="1 2 3 4 5"
sign() { python3 harness/run_sign.py "$@"; }
inject() { python3 harness/run.py "$@"; }

for stage in $STAGES; do
  echo "== stage $stage $(date +%T)"
  case $stage in
  policies)
    # Signing delay of the policies on the region-safe placement, five runs each.
    for rep in $REPS; do
      sign bench --out "$D/policies/rep$rep" --configs frost:2:5:pre \
        --policies fastest,fastestn1.5,hedge3,hedge4,hedge3d1.5 --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
      sign bench --out "$D/policies/rep$rep" --configs frost:3:5:pre \
        --policies fastest,fastestn1.5,hedge4,hedge5,hedge4d1.5 --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
      sign bench --out "$D/policies/rep$rep" --configs frost:4:5:pre \
        --policies fastest,fastestn1.5,hedge5,hedge5d1.5 --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
      sign bench --out "$D/policies/rep$rep" --configs rsa:3:5 \
        --policies fastest,fastestn1.5,first,firstn1.5 --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
    done
    ;;
  newplace)
    # Two placements with signer RTTs of 10, 40 and 120 ms. Neither was
    # measured before; the predictions were written before this stage ran.
    for rep in 1 2; do
      for placement in 10/10/40/40/120 10/40/40/120/120; do
        sign bench --out "$D/newplace/rep$rep" --configs frost:2:5:pre \
          --policies fastest,fastestn1.5,hedge3,hedge4,hedge5,hedge3d1.5,hedge4d1.5,hedge5d1.5 \
          --transports quic4 --rtts $placement --losses 2 --n 1000
        sign bench --out "$D/newplace/rep$rep" --configs frost:3:5:pre \
          --policies fastest,fastestn1.5,hedge4,hedge5,hedge4d1.5,hedge5d1.5 \
          --transports quic4 --rtts $placement --losses 2 --n 1000
        sign bench --out "$D/newplace/rep$rep" --configs frost:4:5:pre \
          --policies fastest,fastestn1.5,hedge5,hedge5d1.5 \
          --transports quic4 --rtts $placement --losses 2 --n 1000
      done
    done
    ;;
  handshake)
    # The region-safe configuration as real handshakes, five runs each.
    for rep in $REPS; do
      sign handshake --out "$D/handshake/rep$rep" --configs frost:3:5:pre --policies fastest,hedge4,hedge5 \
        --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
      sign handshake --out "$D/handshake/rep$rep" --configs rsa:3:5 --policies fastest,firstn1.5 \
        --transports quic4 --rtts 5/5/20/20/80 --losses 2 --n 1000
    done
    ;;
  clientloss)
    # Loss on the client link with an injected signing delay, 2,000
    # handshakes per cell instead of 400.
    inject --out "$D/clientloss" --servers quic-go,aioquic,quiche,s2n-quic --delays 0,20,100 --rtts 20 --losses 2 --n 2000
    ;;
  *)
    echo "unknown stage $stage" >&2
    exit 2
    ;;
  esac
done
echo "== repeat finished $(date +%T)"
