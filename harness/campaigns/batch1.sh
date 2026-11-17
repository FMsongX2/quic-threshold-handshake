#!/usr/bin/env bash
# Measurement batch of 2026-11-18: what was left of Phases 1 and 2 on the
# servers that already exist, plus the signer-transport follow-ups.
#
# usage: batch1.sh RESULTS_ROOT
# Run from the repository root. Every stage writes its own directory under
# RESULTS_ROOT and skips cells that already have a meta.json, so the script
# can be restarted.
set -u
ROOT=${1:?results root}
D="$ROOT/2026-11-18"
BIG_CLIENT=6,7,14,15
PLACEMENT=50/5/100/1/20
sign() { python3 harness/run_sign.py "$@"; }

echo "== stage quic-conns $(date +%T)"
# How many QUIC connections per signer a burst of handshakes needs.
for conc in 30 100 300; do
  sign handshake --out "$D-quic-conns/conc$conc" --configs frost:3:5:pre --policies fastest \
    --transports quic,quic2,quic3,quic4,quic8 --rtts $PLACEMENT --losses 0,2 \
    --conc $conc --n $((conc * 50)) --client-cpus $BIG_CLIENT
done
sign handshake --out "$D-quic-conns/conc100" --configs frost:3:5:pre --policies fastest \
  --transports quic,quic2,quic3,quic4,quic8 --rtts $PLACEMENT --losses 1,5 \
  --conc 100 --n 5000 --client-cpus $BIG_CLIENT
sign handshake --out "$D-quic-conns/conc100-far" --configs frost:3:5:pre --policies fastest \
  --transports quic,quic2,quic4,quic8,quic16 --rtts 50 --losses 0,2 \
  --conc 100 --n 5000 --client-cpus $BIG_CLIENT

echo "== stage system $(date +%T)"
# Front server cores and concurrent handshakes.
SYSTEM=plain-ed25519:1:1,frost:3:5,frost:3:5:pre,plain-rsa:1:1,rsa:3:5
for cpus in 4 4,5 4,5,12,13; do
  tag=cpu$(echo $cpus | tr -cd , | wc -c | awk '{print $1 + 1}')
  sign handshake --out "$D-system/${tag}_conc1" --configs $SYSTEM --policies fastest --transports quic4 \
    --rtts 5 --n 200 --conc 1 --server-cpus $cpus
  sign handshake --out "$D-system/${tag}_conc10" --configs $SYSTEM --policies fastest --transports quic4 \
    --rtts 5 --n 1000 --conc 10 --server-cpus $cpus --client-cpus $BIG_CLIENT
  sign handshake --out "$D-system/${tag}_conc100" --configs $SYSTEM --policies fastest --transports quic4 \
    --rtts 5 --n 5000 --conc 100 --server-cpus $cpus --client-cpus $BIG_CLIENT
done

echo "== stage signer-capacity $(date +%T)"
# Offered handshake rate against the share rate one signer can compute.
for cores in 1 2; do
  for rate in 100 200 250 300 400 600; do
    sign handshake --out "$D-signer-capacity/rsa_cores${cores}_rate$rate" --configs rsa:2:3 --policies fastest \
      --transports quic4 --rtts 5 --rate $rate --n $((rate * 10)) --signer-cpus $cores \
      --server-cpus 4,5,12,13 --client-cpus $BIG_CLIENT
  done
done
for rate in 200 400 800 1600; do
  sign handshake --out "$D-signer-capacity/frost_cores1_rate$rate" --configs frost:2:3:pre --policies fastest \
    --transports quic4 --rtts 5 --rate $rate --n $((rate * 10)) \
    --server-cpus 4,5,12,13 --client-cpus $BIG_CLIENT
done

echo "== stage signer-events $(date +%T)"
# A signer in every policy's set (signer 2, RTT 5 ms) fails or slows down
# 15 s into a 40 s run at 20 handshakes per second.
for transport in quic4 persistent16; do
  for event in stop pause; do
    sign handshake --out "$D-signer-events/${transport}_$event" --configs frost:3:5:pre \
      --policies fixed,fastest,fastestn1.5,hedge,hedged1.5,hedge4d1.5b0.2 --transports $transport \
      --rtts $PLACEMENT --rate 20 --n 800 --events "15:$event:2"
    sign handshake --out "$D-signer-events/${transport}_$event" --configs rsa:3:5 \
      --policies fastest,first --transports $transport \
      --rtts $PLACEMENT --rate 20 --n 800 --events "15:$event:2"
  done
  sign handshake --out "$D-signer-events/${transport}_slow" --configs frost:3:5:pre \
    --policies fixed,fastest,fastestn1.5,hedge,hedged1.5,hedge4d1.5b0.2 --transports $transport \
    --rtts $PLACEMENT --rate 20 --n 800 --events "15:rtt:2:200"
  sign handshake --out "$D-signer-events/${transport}_slow" --configs rsa:3:5 \
    --policies fastest,first --transports $transport \
    --rtts $PLACEMENT --rate 20 --n 800 --events "15:rtt:2:200"
done

echo "== stage network $(date +%T)"
# RTT_c x RTT_s x scheme with one handshake at a time.
ALL=plain-ed25519:1:1,frost:2:3,frost:3:5,frost:2:3:pre,frost:3:5:pre,plain-rsa:1:1,rsa:2:3,rsa:3:5
for rttc in 1 20 100; do
  sign handshake --out "$D-network/rttc$rttc" --configs $ALL --policies fastest --transports quic4 \
    --rtts 0,5,50 --n 200 --client-rtt $rttc
done

echo "== stage network-loss $(date +%T)"
LOSSY=plain-ed25519:1:1,frost:3:5,frost:3:5:pre,rsa:3:5
sign handshake --out "$D-network-loss/signer" --configs $LOSSY --policies fastest --transports quic4 \
  --rtts 5,50 --losses 1,2 --n 400
sign handshake --out "$D-network-loss/client" --configs $LOSSY --policies fastest --transports quic4 \
  --rtts 5,50 --n 400 --client-loss 1
sign handshake --out "$D-network-loss/both" --configs $LOSSY --policies fastest --transports quic4 \
  --rtts 5,50 --losses 1 --n 400 --client-loss 1

echo "== stage jitter $(date +%T)"
for jitter in 0 10 30; do
  sign bench --out "$D-jitter/jitter$jitter" --configs frost:3:5:pre,rsa:3:5 --policies fastest,fastestn1.5 \
    --transports quic4,persistent16 --rtts $PLACEMENT --n 1000 --signer-jitter-pct $jitter
done

echo "== stage p1-rate $(date +%T)"
# Handshake rate each server sustains with an injected signing delay.
python3 harness/run.py --out "$D-p1-rate" --servers quic-go,aioquic,quiche --delays 0,5,20 --rtts 20 \
  --concs "" --rates 50,100,200,400,800,1600 --rate-seconds 10 --client-cpus $BIG_CLIENT

echo "== stage p1-rttc $(date +%T)"
python3 harness/run.py --out "$D-p1-rttc" --servers quic-go,aioquic,quiche \
  --delays 0,1,5,20,50,100,200,500,1200 --rtts 1,100 --n 100

echo "== stage p1-loss $(date +%T)"
python3 harness/run.py --out "$D-p1-loss" --servers quic-go,aioquic,quiche \
  --delays 0,20,100 --rtts 1,20,100 --losses 2 --n 400

echo "== batch1 finished $(date +%T)"
