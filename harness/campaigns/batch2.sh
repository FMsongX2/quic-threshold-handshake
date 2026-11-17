#!/usr/bin/env bash
# Measurement batch 2 of 2026-11-18: real signers behind every QUIC server,
# the s2n-quic server in the delay-injection grid, and the second client.
#
# usage: batch2.sh RESULTS_ROOT [STAGE ...]
# Run from the repository root. Without stage names every stage runs. Cells
# that already have a meta.json are skipped, so the script can be restarted.
set -u
ROOT=${1:?results root}
shift
STAGES=${*:-impl-signer impl-client-loss p1-s2n client-s2n}
D="$ROOT/2026-11-18"
BIG_CLIENT=6,7,14,15
SERVERS="quic-go quic-go-coordd aioquic quiche s2n-quic"
sign() { python3 harness/run_sign.py "$@"; }
inject() { python3 harness/run.py "$@"; }

for stage in $STAGES; do
  echo "== stage $stage $(date +%T)"
  case $stage in
  impl-signer)
    # The same coordinator and signers behind each server implementation.
    CONFIGS=plain-ed25519:1:1,frost:3:5:pre,frost:3:5,plain-rsa:1:1,rsa:3:5
    for server in $SERVERS; do
      sign handshake --out "$D-impl-signer/${server}_conc1" --server $server --configs $CONFIGS --policies fastest \
        --transports quic4 --rtts 5,50 --n 200 --conc 1
      sign handshake --out "$D-impl-signer/${server}_conc10" --server $server --configs $CONFIGS --policies fastest \
        --transports quic4 --rtts 5,50 --n 500 --conc 10 --client-cpus $BIG_CLIENT
      sign handshake --out "$D-impl-signer/${server}_conc100" --server $server --configs $CONFIGS --policies fastest \
        --transports quic4 --rtts 5,50 --n 2000 --conc 100 --client-cpus $BIG_CLIENT
    done
    ;;
  impl-client-loss)
    # Loss on the client link while the server waits for the signers.
    for server in $SERVERS; do
      sign handshake --out "$D-impl-client-loss/$server" --server $server --configs plain-ed25519:1:1,frost:3:5:pre \
        --policies fastest --transports quic4 --rtts 5,50 --n 400 --client-loss 2
    done
    ;;
  p1-s2n)
    # The delay-injection measurements of the other servers, for s2n-quic.
    inject --out "$D-p1-s2n/sweep" --servers s2n-quic --delays 0,1,5,20,50,100,200,500,1200 --rtts 1,20,100 --n 100
    inject --out "$D-p1-s2n/concurrency" --servers s2n-quic --delays 0,50 --rtts 20 --concs 1,10,100 --n 50 \
      --client-cpus $BIG_CLIENT
    inject --out "$D-p1-s2n/rate" --servers s2n-quic --delays 0,5,20 --rtts 20 --concs "" \
      --rates 50,100,200,400,800,1600 --rate-seconds 10 --client-cpus $BIG_CLIENT
    inject --out "$D-p1-s2n/loss" --servers s2n-quic --delays 0,20,100 --rtts 1,20,100 --losses 2 --n 400
    ;;
  client-s2n)
    # The second client against every server.
    ALL=quic-go,aioquic,quiche,s2n-quic
    inject --out "$D-client-s2n/sweep" --client s2n-quic --servers $ALL --delays 0,20,50,100,200,500,1200 --rtts 20 --n 100
    inject --out "$D-client-s2n/concurrency" --client s2n-quic --servers $ALL --delays 50 --rtts 20 --concs 100 --n 50 \
      --client-cpus $BIG_CLIENT
    inject --out "$D-client-s2n/loss" --client s2n-quic --servers $ALL --delays 0,100 --rtts 20 --losses 2 --n 400
    for server in quic-go quiche; do
      sign handshake --out "$D-client-s2n/signer_$server" --client s2n-quic --server $server \
        --configs frost:3:5:pre,rsa:3:5 --policies fastest --transports quic4 --rtts 5,50 --n 200
    done
    ;;
  *)
    echo "unknown stage $stage" >&2
    exit 2
    ;;
  esac
done
echo "== batch2 finished $(date +%T)"
