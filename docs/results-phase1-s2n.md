# Phase 1 결과: s2n-quic 서버와 두 번째 client

측정일은 2026년 11월 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 측정 전의 예측은 [worklog.md](worklog.md)의 "남은 측정의 예측"에 있다. 원자료는 `results/2026-11-18-p1-s2n`, `results/2026-11-18-client-s2n`에 있다.

## 1. 배경과 방법

서명 지연을 주입한 측정([results-phase1-pilot.md](results-phase1-pilot.md), [results-phase1-grid.md](results-phase1-grid.md))은 서버 3개(quic-go, aioquic, quiche)와 client 하나(quic-go)로 했다. 설계는 서버 구현체 4개와 client 구현체 2개를 요구한다. 이 문서는 네 번째 서버인 s2n-quic과 두 번째 client를 같은 방법으로 측정한다.

- s2n-quic 서버는 TLS library로 s2n-tls를 쓰며, s2n-tls의 비동기 private key 연산으로 서명을 정해진 시간 뒤에 끝낸다. 서명을 기다리는 동안 서버는 다른 연결을 계속 처리한다. 인증서 key는 다른 서버와 같은 ECDSA P-256이다. 이 경로는 key exchange로 X25519를 협상한다. 나머지 서버는 X25519MLKEM768을 협상하므로 ServerHello의 크기가 다르다.
- 두 번째 client는 s2n-quic에 rustls를 붙여 만들었다. quic-go client와 같은 key share(X25519MLKEM768, X25519)를 보내고 연결마다 socket을 새로 연다. qlog는 남기지 않는다.

이 문서에서 "지연"은 서버가 서명하기 전에 기다리도록 주입한 시간이다. 서버에는 CPU 2개를 주었다. 따로 적지 않으면 RTT_c는 20 ms이고 loss는 없다.

## 2. 요약

1. s2n-quic 서버에서도 T_hs 중앙값은 주입한 지연만큼만 늘었다. RTT_c 1, 20, 100 ms와 지연 0~1,200 ms의 27개 조건에서 차이는 −0.18~0.60 ms다.
2. s2n-quic 서버는 handshake가 몰려도 T_hs를 유지했다. 지연 50 ms, 동시 handshake 100개에서 중앙값이 72.3 ms였고, 초당 1,600개의 rate에서도 T_hs가 변하지 않았다. 네 서버 가운데 quic-go와 함께 가장 안정적이었다.
3. s2n-quic 서버는 client link의 loss에 가장 약했다. loss 2%, RTT_c 20 ms에서 handshake의 2~3%가 900 ms 넘게 늦어졌다. 서버의 ServerHello가 손실되면 client가 Initial을 다시 보내도 서버는 ACK만 돌려주었고, ServerHello는 자신의 timer가 끝난 뒤에 다시 보냈다.
4. 두 번째 client로 재도 T_hs는 네 서버 모두에서 주입한 지연만큼만 늘었다(차이 −0.05~0.89 ms). 실제 signer를 붙인 handshake에서 T_hs에서 D_sign을 뺀 값은 20.5~20.8 ms였다. 서명 지연이 T_hs에 그대로 더해진다는 결과는 client 구현체에 달려 있지 않다.
5. client의 probe timeout 초깃값이 꼬리의 크기를 정했다. 두 번째 client는 RTT 표본이 없을 때 약 1초를 기다린다(quic-go client는 200 ms). client link의 loss 2%에서 이 client는 네 서버 모두에서 handshake의 1~3.5%가 약 1초 늦어졌다.
6. 두 번째 client로 handshake 100개를 함께 시작하면, loss를 주지 않았는데도 quiche 서버에서 handshake의 49%가, s2n-quic 서버에서 6%가 약 1.09초 걸렸다. quic-go 서버에서는 그런 handshake가 없었다. 원인은 확인하지 못했다.

## 3. s2n-quic 서버

client는 quic-go다.

### 3.1 handshake를 하나씩 할 때

조건마다 100회이고 전부 성공했다. 각 칸은 T_hs 중앙값(ms)이다.

| RTT_c | 지연 0 | 5 | 20 | 50 | 100 | 200 | 500 | 1,200 ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 ms | 2.66 | 7.88 | 22.98 | 53.01 | 103.02 | 202.63 | 503.26 | 1,202.97 |
| 20 ms | 21.67 | 26.76 | 41.93 | 71.87 | 121.49 | 221.56 | 521.82 | 1,221.97 |
| 100 ms | 101.68 | 106.76 | 121.73 | 151.79 | 201.90 | 302.02 | 601.78 | 1,302.20 |

- "T_hs 중앙값 − 지연이 없을 때의 중앙값 − 지연"은 지연 1 ms를 포함한 27개 조건에서 −0.18~0.60 ms였다.
- 지연이 없을 때의 T_hs는 quic-go와 quiche 서버보다 0.7~1.0 ms 길었다.
- s2n-quic은 client의 Initial을 받자마자 ACK를 보낸다. client가 처음 받은 packet은 1.2~1.3 ms(RTT_c 1 ms), 20.3 ms, 100.3 ms에 도착했고 client의 첫 RTT 표본은 RTT_c와 같았다.
- client가 handshake를 마칠 때까지 보낸 packet은 평균 4~5개였고 지연이 1,200 ms여도 5개였다. client의 probe timeout은 한 번도 끝나지 않았다. RTT_c 1 ms, 지연 1,200 ms에서 quiche 서버는 12개, aioquic 서버는 18개였다([results-phase1-grid.md](results-phase1-grid.md) 3절). s2n-quic 서버에서 client가 probe를 보내지 않은 까닭은 확인하지 않았다.

### 3.2 동시 handshake와 rate

동시 handshake는 조건마다 50개(1, 10)와 500개(100)다. 각 칸은 T_hs의 중앙값 / p99(ms)다.

| 지연 | 동시 1개 | 10개 | 100개 |
| --- | --- | --- | --- |
| 0 | 21.6 / 22.3 | 22.0 / 22.9 | 22.8 / 31.4 |
| 50 ms | 72.0 / 72.6 | 72.1 / 73.5 | 72.3 / 92.5 |

같은 조건(지연 50 ms, 동시 100개)의 중앙값은 quic-go 74 ms, quiche 73 ms, aioquic 3,228 ms였다([results-phase1-pilot.md](results-phase1-pilot.md)).

정해진 rate로 handshake를 10초 동안 시작한 결과다. 각 칸은 그 10초 안에 완료된 handshake의 초당 수 / T_hs 중앙값(ms)이다.

| 지연 | rate 50 | 200 | 800 | 1,600 |
| --- | --- | --- | --- | --- |
| 0 | 50 / 21.5 | 200 / 21.4 | 798 / 21.4 | 1,596 / 21.4 |
| 5 ms | 50 / 26.4 | 200 / 26.2 | 798 / 26.2 | 1,596 / 26.3 |
| 20 ms | 50 / 41.6 | 199 / 41.6 | 797 / 41.5 | 1,593 / 41.5 |

모든 조건에서 handshake가 전부 성공했다. rate 1,600에서 마지막 2초에 시작한 handshake의 중앙값은 21.4, 26.3, 41.4 ms로 처음과 같았고 p99는 22.3, 27.3, 42.3 ms였다. quiche 서버는 같은 rate에서 중앙값이 2.5~3.3 ms 늘었다. s2n-quic 서버는 worker thread를 여러 개 쓰고 quiche 서버는 QUIC 처리를 thread 하나로 한다.

### 3.3 client link의 loss

방향마다 loss 2%를 주고 handshake를 하나씩 400회 했다. 전부 성공했다. T_hs 열은 중앙값 / p95 / p99(ms)다. 뒤의 두 열은 중앙값보다 150 ms 넘게, 900 ms 넘게 늦어진 handshake의 수다.

| RTT_c | 지연 | T_hs | 150 ms 넘게 | 900 ms 넘게 |
| --- | --- | --- | --- | --- |
| 1 ms | 0 | 2.7 / 6.9 / 322.4 | 13 | 1 |
| 1 ms | 20 ms | 23.0 / 26.4 / 222.4 | 5 | 0 |
| 1 ms | 100 ms | 103.0 / 106.0 / 108.5 | 4 | 0 |
| 20 ms | 0 | 21.7 / 83.0 / 1,021.0 | 12 | 11 |
| 20 ms | 20 ms | 41.9 / 102.2 / 1,144.6 | 12 | 12 |
| 20 ms | 100 ms | 122.0 / 183.3 / 1,056.0 | 8 | 8 |
| 100 ms | 0 | 101.7 / 402.5 / 1,100.5 | 27 | 9 |
| 100 ms | 20 ms | 121.8 / 422.2 / 1,121.2 | 25 | 11 |
| 100 ms | 100 ms | 201.9 / 502.7 / 1,224.8 | 33 | 17 |

RTT_c 20 ms에서 150 ms 넘게 늦어진 handshake는 quic-go 서버 4~13개, aioquic 서버 0~8개, quiche 서버 0개였다([results-phase1-grid.md](results-phase1-grid.md) 4절). s2n-quic 서버는 수가 비슷하거나 많고, 늦어진 크기가 약 1초로 훨씬 크다.

늦어진 handshake 4개의 client qlog를 열었다. 순서는 넷 모두 같았다.

1. 서버의 Initial(ServerHello)이 손실됐다. client는 Handshake packet을 받았지만 key가 없어 쌓아 두었다.
2. client는 RTT 표본이 없어 200 ms 뒤에 Initial을 다시 보냈다. 서버는 ACK만 돌려주었다. client는 이 ACK로 RTT 표본을 얻었다.
3. client는 probe timeout마다 PING을 보냈고 서버는 그때마다 ACK만 돌려주었다.
4. 서버가 PING을 보냈고, 그 ACK를 받은 뒤에 ServerHello를 다시 보냈다. handshake를 시작한 지 RTT_c 1 ms에서는 283~286 ms, RTT_c 20 ms에서는 871~1,019 ms가 된 때였다.
5. 그 뒤 Handshake packet을 다시 받는 데 36~125 ms가 더 걸렸다.

client가 ClientHello를 다시 보내면 quiche와 quic-go 서버는 ServerHello를 곧바로 다시 보낸다. s2n-quic 서버는 그렇게 하지 않고 자신의 probe timeout을 기다렸다. s2n-quic의 RTT 초깃값은 333 ms여서 표본이 없을 때의 probe timeout이 999 ms다([results-phase2-implementations.md](results-phase2-implementations.md) 5절). RTT_c 1 ms에서 서버가 약 283 ms에 다시 보낸 까닭은 확인하지 않았다.

Ed25519 key로 rustls를 쓰는 경로에서도 같은 크기의 꼬리가 나왔다([results-phase2-implementations.md](results-phase2-implementations.md) 5절). TLS library가 아니라 s2n-quic의 손실 복구에서 나오는 동작이다.

## 4. 두 번째 client

client는 s2n-quic(rustls)이다.

### 4.1 handshake를 하나씩 할 때

조건마다 100회이고 전부 성공했다. 각 칸은 T_hs 중앙값(ms)이다.

| 서버 | 지연 0 | 20 | 50 | 100 | 200 | 500 | 1,200 ms |
| --- | --- | --- | --- | --- | --- | --- | --- |
| quic-go | 20.67 | 40.84 | 71.20 | 121.56 | 220.95 | 521.25 | 1,221.38 |
| aioquic | 21.21 | 41.36 | 71.41 | 121.43 | 221.48 | 521.73 | 1,221.79 |
| quiche | 20.55 | 40.66 | 70.81 | 120.91 | 221.34 | 521.01 | 1,221.17 |
| s2n-quic | 21.47 | 41.62 | 71.66 | 121.81 | 221.91 | 521.42 | 1,221.77 |

"T_hs 중앙값 − 지연이 없을 때의 중앙값 − 지연"은 24개 조건에서 −0.05~0.89 ms였다. 지연이 없을 때의 T_hs는 quic-go client로 잰 값과 0.3 ms 안에서 같았다.

### 4.2 실제 signer

quic-go와 quiche 서버에 실제 signer를 붙이고 handshake를 하나씩 200회 했다. signer 구성은 [results-phase2-implementations.md](results-phase2-implementations.md) 1절과 같다. 전부 성공했다.

| 서버 | scheme | RTT_s | T_hs 중앙값 | D_sign 중앙값 | T_hs − D_sign |
| --- | --- | --- | --- | --- | --- |
| quic-go | precomputation FROST (3, 5) | 5 ms | 26.2 ms | 5.5 ms | 20.70 ms |
| quic-go | precomputation FROST (3, 5) | 50 ms | 71.3 ms | 50.6 ms | 20.75 ms |
| quic-go | threshold RSA (3, 5) | 5 ms | 31.0 ms | 10.3 ms | 20.68 ms |
| quic-go | threshold RSA (3, 5) | 50 ms | 77.9 ms | 57.2 ms | 20.67 ms |
| quiche | precomputation FROST (3, 5) | 5 ms | 26.1 ms | 5.6 ms | 20.51 ms |
| quiche | precomputation FROST (3, 5) | 50 ms | 71.1 ms | 50.6 ms | 20.53 ms |
| quiche | threshold RSA (3, 5) | 5 ms | 32.7 ms | 12.2 ms | 20.52 ms |
| quiche | threshold RSA (3, 5) | 50 ms | 75.0 ms | 54.5 ms | 20.57 ms |

T_hs에서 D_sign을 뺀 값은 quic-go client로 잰 값(quic-go 서버 20.96~21.08 ms, quiche 서버 20.77~20.86 ms)보다 0.2~0.4 ms 작았다.

### 4.3 client link의 loss

방향마다 loss 2%를 주고 handshake를 하나씩 400회 했다. 전부 성공했다. T_hs 열은 중앙값 / p95 / p99(ms)다.

| 지연 | 서버 | T_hs | 150~250 ms 늦어짐 | 900 ms 넘게 늦어짐 |
| --- | --- | --- | --- | --- |
| 0 | quic-go | 20.6 / 81.9 / 421.8 | 10 | 4 |
| 0 | aioquic | 21.2 / 22.5 / 1,041.2 | 0 | 5 |
| 0 | quiche | 20.6 / 21.5 / 1,040.4 | 0 | 5 |
| 0 | s2n-quic | 21.5 / 23.0 / 1,041.0 | 0 | 10 |
| 100 ms | quic-go | 121.1 / 182.3 / 1,140.8 | 6 | 5 |
| 100 ms | aioquic | 121.5 / 122.7 / 1,141.6 | 0 | 9 |
| 100 ms | quiche | 121.0 / 121.4 / 182.7 | 0 | 4 |
| 100 ms | s2n-quic | 121.8 / 181.9 / 1,141.3 | 1 | 14 |

- 네 서버 모두에서 handshake의 1~3.5%가 약 1초 늦어졌다(중앙값보다 1,020 ms 안팎). quic-go client에서는 quiche 서버에 이런 handshake가 없었다. 두 번째 client는 qlog를 남기지 않아 어떤 packet이 손실된 경우인지 확인하지 못했다. 크기는 이 client가 RTT 표본 없이 기다리는 probe timeout(RTT 초깃값 333 ms의 3배)과 같다.
- quic-go 서버에서만 약 200 ms 늦어진 handshake가 6~10개 있었다. client가 1초를 기다리는 동안 서버가 먼저 다시 보낸 경우로 본다. quic-go는 RTT 표본이 없을 때의 probe timeout이 200 ms다. quic-go client로 잰 측정에서는 client와 서버의 timer가 같은 200 ms여서 둘을 구분할 수 없었다.
- s2n-quic 서버는 약 1초 늦어진 handshake가 10~14개로 가장 많았다. 3.3절의 동작이 더해진 것으로 본다.

### 4.4 동시 handshake 100개

지연 50 ms에서 handshake 100개를 함께 시작하는 것을 5회 반복했다. loss는 주지 않았다. 500개가 전부 성공했다.

| 서버 | T_hs 중앙값 | p95 | p99 | 500 ms 넘게 걸린 handshake |
| --- | --- | --- | --- | --- |
| quic-go | 76.4 ms | 80.1 ms | 80.7 ms | 0 |
| quiche | 90.6 ms | 1,094.1 ms | 1,095.0 ms | 246 (5회 모두) |
| s2n-quic | 74.2 ms | 1,090.3 ms | 1,093.2 ms | 28 (5회 중 2회) |
| aioquic | 3,138.8 ms | 5,434.9 ms | 7,623.4 ms | 455 |

quiche와 s2n-quic 서버에서 500 ms를 넘긴 handshake는 모두 1,090~1,112 ms에 끝났다. 지연이 없을 때보다 약 1초 길다. loss를 주지 않은 조건이므로 host 안에서 packet이 버려졌고 client가 probe timeout 1초 뒤에 복구한 것으로 추정한다. 어디에서 버려졌는지는 확인하지 못했다. quic-go client로 같은 조건을 쟀을 때 quiche 서버의 중앙값은 73 ms였다. quiche 서버의 중앙값 90.6 ms는 절반 가까이가 1초를 넘긴 분포의 가운데 값이어서 대표값으로 쓰기 어렵다.

aioquic의 값은 서명을 차례로 기다리는 동작이며 quic-go client로 잰 값(3,228 ms)과 같다.

## 5. 예측과 판정

| 예측 | 결과 | 판정 |
| --- | --- | --- |
| s2n-quic 서버에서 T_hs는 RTT_c 1, 20, 100 ms 모두에서 주입한 지연만큼만 늘어난다(차이 1 ms 미만). | −0.18~0.60 ms | 맞음 |
| 지연 50 ms, 동시 100개에서 T_hs 중앙값은 quiche와 비슷한 75~85 ms다. | 72.3 ms. 서로 막지 않는 것은 맞았고 값은 예측 범위보다 2.7 ms 낮았다. | 대체로 맞음 |
| 초당 1,600개까지 지연 0, 5, 20 ms 모두 포화하지 않는다. | 전부 처리했고 T_hs가 변하지 않았다. | 맞음 |
| client link의 loss 2%에서 TLS library와 무관하게 handshake의 3~5%가 700~1,000 ms 늦어진다. | s2n-tls 경로에서 RTT_c 20 ms일 때 2~3%, 100 ms일 때 2.3~4.3%가 900 ms 넘게 늦어졌다. RTT_c 1 ms에서는 0~3%가 약 300 ms 늦어졌다. | 일부 맞음 |
| 두 번째 client로 재도 T_hs는 주입한 지연만큼만 늘어난다(차이 1.5 ms 미만). | −0.05~0.89 ms | 맞음 |
| 두 번째 client에서 quic-go 서버는 ServerHello가 손실되면 약 1초 늦어지고, quiche는 RTT_c의 3배쯤만 늦어진다. | quic-go 서버는 약 200 ms 뒤에 스스로 다시 보냈다. 약 1초 늦어진 handshake는 quiche를 포함한 네 서버 모두에서 1~3.5%였다. | 틀림 |
| 두 번째 client로 실제 signer를 붙인 handshake의 T_hs − D_sign은 20~22 ms다. | 20.51~20.75 ms | 맞음 |

4.4절의 결과는 예측에 없었다.

## 6. 한계

- 두 번째 client는 qlog를 남기지 않는다. 4.3절과 4.4절에서 약 1초 늦어진 handshake의 원인을 packet 단위로 확인하지 못했다.
- 4.4절은 조건 하나(지연 50 ms)를 5회 반복한 것이다. 지연이 없는 조건과 다른 동시 수는 측정하지 않았다.
- 두 번째 client로는 RTT_c 20 ms만 측정했고, 정해진 rate의 부하는 이 client가 지원하지 않는다.
- s2n-quic 서버의 지연 주입 경로는 key exchange로 X25519를 협상한다. 다른 서버와 ServerHello의 크기가 달라 지연이 없을 때의 T_hs를 그대로 비교할 수 없다.
- loss 조건은 400회씩이고 qlog로 확인한 것은 s2n-quic 서버의 handshake 4개다.
- s2n-quic 서버가 손실된 ServerHello를 다시 보내는 시점을 source로 확인하지 않았다.
- 측정 중 host의 1분 load average는 1.4~3.8이었다.

## 7. 설계에 주는 영향

- 서명 지연이 T_hs에 그대로 더해진다는 결과는 서버 구현체 4개와 client 구현체 2개의 조합에서 유지된다. 서명 구성을 고를 때 QUIC 구현체를 따로 고려하지 않아도 된다.
- 구현체는 handshake가 몰릴 때와 loss가 있을 때 차이를 만든다. s2n-quic은 몰림에 가장 강했고 loss에 가장 약했다. quiche는 loss에 가장 강했다. aioquic은 몰림을 견디지 못했다.
- loss가 있는 link에서 꼬리의 크기는 client와 서버가 RTT 표본 없이 기다리는 시간이 정한다. 이 측정의 구현체에서 그 값은 200 ms(quic-go)와 약 1초(s2n-quic)였다. threshold signing이 더하는 수십 ms보다 크다.
