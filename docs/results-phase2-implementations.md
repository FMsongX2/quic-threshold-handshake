# Phase 2 결과: 서버 구현체별 실제 signer

측정일은 2026년 11월 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 원자료는 `results/2026-11-18-impl-signer`, `results/2026-11-18-impl-client-loss`에 있다.

## 1. 배경과 방법

[results-phase2-signer.md](results-phase2-signer.md)와 [results-phase2-environment.md](results-phase2-environment.md)는 실제 signer를 quic-go 서버에만 붙였다. 서명 지연을 주입한 측정([results-phase1-pilot.md](results-phase1-pilot.md))에서는 서버 구현체의 차이가 동시 handshake 조건에서 나타났다. 이 문서는 같은 coordinator와 signer를 네 서버 구현체에 붙여, 그 차이가 실제 서명 지연에서도 나타나는지 확인한다.

서버는 다섯 가지다.

| 표기 | QUIC 구현체 | coordinator의 위치 | 서명을 기다리는 방식 |
| --- | --- | --- | --- |
| quic-go | quic-go | 서버 process 안 | handshake마다 goroutine이 기다린다. |
| quic-go + daemon | quic-go | 같은 container의 daemon | 위와 같다. |
| aioquic | aioquic | daemon | 서명 호출이 event loop를 멈춘다. |
| quiche | Cloudflare quiche | daemon | BoringSSL의 비동기 private key method로 요청만 보내고 event loop로 돌아간다. |
| s2n-quic | s2n-quic | daemon | Ed25519 key는 rustls를 쓰며 handshake마다 TLS 처리를 별도 thread에 맡긴다. RSA key는 s2n-tls의 비동기 private key 연산을 쓴다. |

daemon은 서버가 넘긴 서명 요청을 받아 signer들에게 보내고 서명을 돌려준다. quic-go의 두 줄을 비교하면 daemon을 거치는 비용이 나온다.

공통 조건은 다음과 같다. client는 quic-go다. RTT_c는 20 ms다. coordinator와 signer 사이는 signer당 유지되는 QUIC 연결 4개이고 정책은 fastest다. signer의 RTT_s는 모든 signer에 같은 값(5 ms 또는 50 ms)을 주었다. scheme은 원격 signer 하나의 Ed25519와 RSA, precomputation을 쓰는 FROST (3, 5), FROST (3, 5), threshold RSA (3, 5)다. 서버에는 CPU 2개를 주었다. s2n-quic은 RSA key에서 key exchange로 X25519를, 나머지 조건은 모두 X25519MLKEM768을 협상했다.

이 측정은 예측을 미리 적지 않고 시작했다.

## 2. 요약

1. handshake를 하나씩 할 때 T_hs에서 D_sign을 뺀 값은 다섯 서버가 20.6~21.8 ms 안에 있었다. 실제 서명 지연도 구현체와 무관하게 T_hs에 그대로 더해진다.
2. daemon을 거치는 비용은 handshake를 하나씩 할 때 0.01~0.13 ms, 동시 100개에서 0.4~3.3 ms였다.
3. aioquic은 handshake가 겹치면 서명을 차례로 기다렸다. 동시 10개, D_sign 50 ms에서 T_hs 중앙값이 283~290 ms였고 다른 서버는 71~74 ms였다. 동시 100개에서는 3.2~6.5초였고 handshake의 0.2~0.7%가 실패했다.
4. quic-go, quiche, s2n-quic은 동시 100개에서도 T_hs에서 D_sign을 뺀 값이 21.2~28.9 ms였다. s2n-quic이 21.2~24.7 ms로 가장 작았다.
5. client link에 loss 2%가 있을 때 s2n-quic은 handshake의 3~5%가 360~1,020 ms 늦어졌다. 서버의 handshake packet이 손실되면 client가 PING을 보내도 ACK만 돌려주고, 손실된 packet은 자신의 timer가 끝난 뒤에 다시 보냈다. quiche는 150 ms 넘게 늦어진 handshake가 400개 중 0~2개로 가장 적었다.

## 3. handshake를 하나씩 할 때

조건마다 200회이고 전부 성공했다. 각 칸은 T_hs 중앙값(ms)이다.

| scheme | RTT_s | D_sign | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 5.2 | 26.2 | 26.3 | 26.9 | 26.0 | 26.2 |
| Ed25519 (1, 1) | 50 ms | 50.2 | 71.2 | 71.3 | 71.8 | 71.0 | 71.2 |
| precomputation FROST (3, 5) | 5 ms | 5.5~5.6 | 26.6 | 26.6 | 27.3 | 26.4 | 26.6 |
| precomputation FROST (3, 5) | 50 ms | 50.5~50.6 | 71.7 | 71.6 | 72.2 | 71.4 | 71.6 |
| FROST (3, 5) | 5 ms | 10.8 | 31.8 | 32.0 | 32.4 | 31.6 | 31.8 |
| FROST (3, 5) | 50 ms | 100.8~100.9 | 121.9 | 122.0 | 122.7 | 121.7 | 122.0 |
| RSA (1, 1) | 5 ms | 5.7 | 26.7 | 26.8 | 27.4 | 26.5 | 26.4 |
| RSA (1, 1) | 50 ms | 50.7~50.8 | 71.7 | 71.8 | 72.6 | 71.6 | 71.5 |
| threshold RSA (3, 5) | 5 ms | 10.3~12.2 | 31.4 | 33.2 | 33.9 | 31.0 | 32.7 |
| threshold RSA (3, 5) | 50 ms | 55.2~57.2 | 78.2 | 78.3 | 76.9 | 76.1 | 76.0 |

T_hs 중앙값에서 D_sign 중앙값을 뺀 값을 서버별로 모으면 다음과 같다. 범위는 위 10개 조건에 걸친 값이다.

| 서버 | T_hs − D_sign |
| --- | --- |
| quic-go | 20.96~21.08 ms |
| quic-go + daemon | 21.03~21.18 ms |
| aioquic | 21.61~21.81 ms |
| quiche | 20.77~20.86 ms |
| s2n-quic | 20.60~21.14 ms |

- 서버 사이의 차이는 1.3 ms 안이다. aioquic이 0.6~0.8 ms 길다.
- quic-go에서 daemon을 거치면 T_hs − D_sign이 0.01~0.13 ms 늘었다.
- threshold RSA의 D_sign이 조건마다 2 ms씩 다른 것은 signer의 share 계산 시간이 실행마다 4.9 ms 또는 6.8 ms였기 때문이다. 서버와는 무관하게 나타났고 원인은 확인하지 않았다. T_hs에서 D_sign을 뺀 값에는 영향이 없다.

## 4. 동시 handshake

동시 handshake 10개는 조건마다 500개, 100개는 2,000개를 측정했다. client에는 CPU 4개를 주었다. 각 칸은 T_hs 중앙값 / p99(ms)다.

### 4.1 동시 10개

| scheme | RTT_s | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 26.6 / 27.8 | 27.0 / 30.5 | 54.1 / 82.9 | 26.6 / 28.2 | 26.6 / 29.3 |
| Ed25519 (1, 1) | 50 ms | 71.4 / 72.7 | 72.0 / 74.3 | 287.8 / 534.5 | 71.4 / 72.8 | 71.7 / 73.1 |
| precomputation FROST (3, 5) | 5 ms | 27.7 / 34.8 | 28.0 / 35.8 | 57.2 / 88.2 | 27.5 / 34.9 | 27.6 / 32.6 |
| precomputation FROST (3, 5) | 50 ms | 72.7 / 80.0 | 73.0 / 78.4 | 283.4 / 540.9 | 72.3 / 78.4 | 72.9 / 81.0 |
| FROST (3, 5) | 5 ms | 33.0 / 37.6 | 33.0 / 36.8 | 82.2 / 139.6 | 32.6 / 35.4 | 33.1 / 37.4 |
| FROST (3, 5) | 50 ms | 122.7 / 124.6 | 123.4 / 126.1 | 549.3 / 1,044.3 | 122.4 / 126.2 | 123.0 / 126.4 |
| RSA (1, 1) | 5 ms | 28.7 / 32.5 | 29.1 / 32.5 | 57.0 / 88.3 | 28.8 / 32.9 | 28.7 / 31.7 |
| RSA (1, 1) | 50 ms | 73.7 / 77.1 | 74.2 / 77.9 | 289.4 / 540.4 | 73.8 / 77.5 | 73.5 / 77.0 |
| threshold RSA (3, 5) | 5 ms | 55.8 / 76.3 | 69.8 / 96.1 | 88.1 / 152.6 | 53.3 / 76.4 | 66.9 / 95.0 |
| threshold RSA (3, 5) | 50 ms | 98.8 / 121.6 | 112.5 / 140.9 | 310.6 / 585.2 | 101.1 / 123.3 | 111.6 / 139.7 |

aioquic을 뺀 네 서버는 T_hs에서 D_sign을 뺀 값이 20.7~21.9 ms로, handshake를 하나씩 할 때와 같았다. aioquic은 한 번에 서명 하나만 기다린다. 함께 시작한 handshake 10개가 차례로 끝나므로 중앙값이 "5.5 × (D_sign + c)"에 가까웠고 c는 조건에 따라 −1~5 ms였다. 원격 signer 하나와 precomputation FROST의 D_sign 50 ms에서 283~290 ms, FROST의 100 ms에서 549 ms다.

threshold RSA에서 quic-go + daemon과 s2n-quic의 T_hs가 quic-go와 quiche보다 11~17 ms 긴 것은 그 실행들에서 signer의 share 계산이 6.8 ms였기 때문이다(나머지는 4.9~5.0 ms). 서버의 차이로 읽으면 안 된다.

### 4.2 동시 100개

| scheme | RTT_s | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 31.6 / 40.6 | 34.1 / 41.0 | 639.4 / 1,464.9 | 32.2 / 226.5 | 28.8 / 36.2 |
| Ed25519 (1, 1) | 50 ms | 74.1 / 83.4 | 77.6 / 86.2 | 3,226.7 / 6,583.8 | 74.2 / 84.6 | 72.9 / 80.0 |
| precomputation FROST (3, 5) | 5 ms | 43.8 / 60.4 | 45.6 / 62.6 | 642.0 / 1,468.4 | 45.3 / 62.5 | 39.9 / 53.8 |
| precomputation FROST (3, 5) | 50 ms | 85.1 / 98.3 | 85.6 / 97.4 | 3,179.4 / 6,585.2 | 87.4 / 106.7 | 83.8 / 101.8 |
| FROST (3, 5) | 5 ms | 47.7 / 60.1 | 51.1 / 65.2 | 677.5 / 1,543.8 | 51.4 / 63.6 | 52.8 / 71.3 |
| FROST (3, 5) | 50 ms | 131.0 / 145.9 | 131.6 / 142.7 | 6,531.9 / 13,338.0 | 132.1 / 150.4 | 129.3 / 140.2 |
| RSA (1, 1) | 5 ms | 54.4 / 85.1 | 53.3 / 87.1 | 647.1 / 1,469.1 | 53.2 / 86.9 | 55.7 / 86.9 |
| RSA (1, 1) | 50 ms | 97.0 / 125.1 | 97.3 / 126.8 | 3,228.4 / 6,585.8 | 96.5 / 272.2 | 96.0 / 124.0 |
| threshold RSA (3, 5) | 5 ms | 434.8 / 718.9 | 401.1 / 532.2 | 712.8 / 3,080.5 | 502.6 / 722.2 | 493.9 / 719.1 |
| threshold RSA (3, 5) | 50 ms | 417.3 / 576.1 | 567.4 / 764.6 | 3,191.5 / 7,290.6 | 397.8 / 576.9 | 610.2 / 763.3 |

T_hs 중앙값에서 D_sign 중앙값을 뺀 값은 다음과 같다. 범위는 10개 조건에 걸친 값이다.

| 서버 | 동시 10개 | 동시 100개 | 동시 100개에서 성공한 handshake |
| --- | --- | --- | --- |
| quic-go | 21.2~21.5 ms | 23.9~25.8 ms | 20,000 / 20,000 |
| quic-go + daemon | 21.2~21.9 ms | 25.0~28.9 ms | 20,000 / 20,000 |
| aioquic | 48.9~448.6 ms | 634~6,431 ms | 19,958 / 20,000 |
| quiche | 20.9~21.4 ms | 23.0~27.0 ms | 20,000 / 20,000 |
| s2n-quic | 20.7~21.5 ms | 21.2~24.7 ms | 20,000 / 20,000 |

- quic-go, quiche, s2n-quic은 동시 100개에서 서버가 더한 지연이 0.2~8 ms였다. s2n-quic이 가장 작았다. s2n-quic 서버는 worker thread를 여러 개 쓰고 quiche 서버는 QUIC 처리를 thread 하나로 한다. 서버에 준 CPU는 2개로 같다.
- daemon을 거치는 quic-go는 process 안에 coordinator를 둔 quic-go보다 T_hs − D_sign이 0.4~3.3 ms 길었다(threshold RSA의 두 조건은 signer 계산 시간이 달라 뺐다).
- aioquic에서는 T_hs 중앙값이 RTT_s 5 ms 조건에서 0.64~0.71초, RTT_s 50 ms 조건에서 3.2초(FROST는 6.5초)였다. RTT_s 50 ms 조건 5개에서 handshake 2,000개 중 4~13개가 실패했다.
- threshold RSA는 서버와 무관하게 T_hs가 0.4~0.6초였다. signer의 share 계산이 T_hs를 정한다([results-phase2-environment.md](results-phase2-environment.md) 5절).
- quiche의 두 조건에서 p99가 중앙값보다 176~194 ms 길었다(Ed25519의 RTT_s 5 ms, RSA의 RTT_s 50 ms). threshold RSA를 뺀 나머지 조건에서는 10~34 ms였다. 두 조건의 원인은 확인하지 않았다.

## 5. client link의 loss

client link에 방향마다 loss 2%를 주고 handshake를 하나씩 400회 했다. 20개 조건 모두에서 400회가 전부 성공했다. 각 칸은 T_hs의 중앙값 / p95 / p99(ms)다.

| scheme | RTT_s | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 26.2 / 49.4 / 226.7 | 26.3 / 87.4 / 227.0 | 26.9 / 50.1 / 89.7 | 26.0 / 81.3 / 87.3 | 26.3 / 49.7 / 1,025.9 |
| Ed25519 (1, 1) | 50 ms | 71.2 / 132.4 / 271.9 | 71.3 / 132.5 / 271.8 | 72.0 / 133.1 / 225.6 | 71.0 / 82.1 / 132.5 | 71.3 / 132.6 / 1,071.2 |
| precomputation FROST (3, 5) | 5 ms | 26.6 / 87.6 / 227.0 | 26.7 / 49.7 / 88.1 | 27.4 / 32.3 / 89.0 | 26.3 / 82.1 / 87.8 | 26.6 / 88.3 / 1,026.8 |
| precomputation FROST (3, 5) | 50 ms | 71.6 / 133.9 / 272.6 | 71.7 / 132.9 / 272.8 | 72.3 / 77.5 / 226.3 | 71.4 / 97.6 / 133.1 | 71.7 / 132.7 / 1,071.3 |

중앙값보다 150 ms 넘게 늦어진 handshake의 수(400개 중)는 다음과 같다. 괄호 안은 그중 350 ms 넘게 늦어진 수다.

| scheme | RTT_s | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 5 | 6 | 1 | 0 | 14 (13) |
| Ed25519 (1, 1) | 50 ms | 10 | 7 | 9 | 1 (1) | 17 (14) |
| precomputation FROST (3, 5) | 5 ms | 9 | 2 | 0 | 1 | 20 (19) |
| precomputation FROST (3, 5) | 50 ms | 15 | 11 | 5 | 2 | 14 (12) |

- quic-go는 지연을 주입한 측정과 같은 꼬리를 보였다. ServerHello가 손실되면 client가 RTT 표본 없이 200 ms를 기다린다([results-phase1-grid.md](results-phase1-grid.md) 4절).
- quiche는 늦어진 handshake가 가장 적었다. client의 Initial을 곧바로 확인해 주므로 client가 RTT_c의 3배쯤 뒤에 복구한다.
- aioquic은 D_sign이 5 ms일 때 0~1개, 50 ms일 때 5~9개였다. 지연을 주입한 측정에서 본 것과 같이, 서명이 끝난 뒤에 보낸 packet이 손실되면 서버가 다시 보낼 때까지 기다린다.
- s2n-quic에서는 handshake의 3.5~5%가 150 ms 넘게 늦어졌고 대부분이 350 ms를 넘었다. Ed25519, RTT_s 5 ms 조건에서 그 13개는 중앙값보다 362~1,021 ms 늦었다(362 ms 2개, 726~731 ms 4개, 942~1,021 ms 7개).

s2n-quic의 늦어진 handshake 3개를 client qlog로 확인했다. 서버는 client의 Initial을 곧바로 ACK로 확인해 주었고 client는 RTT 표본을 얻었다. 그 뒤 서버의 Initial이나 Handshake packet이 손실되자 client는 probe timeout마다 PING을 보냈다(handshake를 시작한 지 약 80, 200, 400, 720~770 ms가 된 시점). 서버는 PING마다 ACK만 돌려주었고, 손실된 packet은 750~1,030 ms 시점에 다시 보냈다. client의 probe에 서버가 handshake data를 다시 보내 주는 quiche와 다른 점이다.

s2n-quic 0.92의 RTT 초깃값은 333 ms다(source의 `DEFAULT_INITIAL_RTT`). RFC 9002의 식으로 RTT 표본이 없을 때의 probe timeout은 999 ms가 된다. 서버가 먼저 보낸 packet은 ACK만 담고 있어 서버는 RTT 표본을 얻지 못하고, ServerHello를 보낸 지 약 1초 뒤에 다시 보낸 것으로 본다. 두 handshake에서 서버가 다시 보낸 시점은 ServerHello를 보낸 지 1,000 ms가 된 때였다. 362 ms와 730 ms 안팎에 복구된 handshake의 경로는 확인하지 않았다.

## 6. 한계

- 예측을 미리 적지 않았다.
- client는 quic-go 하나다.
- threshold RSA signer의 share 계산 시간이 실행마다 4.9 ms 또는 6.8 ms로 달랐다. 동시 handshake 조건의 threshold RSA 값은 서버끼리 비교할 수 없다.
- s2n-quic의 Ed25519 조건은 rustls를, RSA 조건은 s2n-tls를 쓴다. RSA 조건은 key exchange group도 다르다(X25519). TLS library와 key exchange의 영향을 서버 구현체의 영향과 나누지 못했다.
- s2n-quic의 rustls 경로에는 TLS offload의 wake-up 누락을 우회하는 code가 들어 있다([worklog.md](worklog.md)). 5절의 꼬리가 이 우회와 관련이 있는지는 확인하지 않았다. s2n-tls 경로의 loss 측정은 별도로 한다.
- loss 조건은 400회씩이고 qlog로 확인한 것은 s2n-quic의 한 조건, handshake 3개다.
- quiche 서버는 thread 하나, s2n-quic은 여러 thread, quic-go는 goroutine을 쓴다. 서버에 준 CPU 수는 같지만 서버 program의 구조가 다르다.
- 측정 중 host의 1분 load average는 1.6~5.6이었다.

## 7. 설계에 주는 영향

- 서명 지연이 T_hs에 그대로 더해진다는 모델은 네 구현체 모두에 쓸 수 있다. 구현체에 따른 상수 차이는 1.3 ms 안이다.
- coordinator를 서버 밖의 daemon으로 두어도 비용은 동시 100개에서 4 ms 안이다. TLS library가 서명을 밖으로 넘길 수 있으면 서버 구현체를 고치지 않고 threshold signing을 붙일 수 있다.
- aioquic처럼 서명 호출이 event loop를 멈추는 서버는 handshake가 겹치는 조건에서 쓸 수 없다. D_sign 50 ms, 동시 10개에서 이미 T_hs가 4배다.
- loss가 있는 client link에서는 구현체의 손실 복구가 꼬리를 정한다. 서명 지연의 최적화로 줄인 수십 ms보다, 서버가 손실된 handshake packet을 언제 다시 보내는지에서 나오는 차이(60 ms, 200 ms, 1초)가 크다.
