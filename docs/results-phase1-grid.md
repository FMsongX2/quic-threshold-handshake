# Phase 1 결과: RTT_c, client link의 loss, handshake rate

측정일은 2026년 11월 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 측정 전의 예측은 [worklog.md](worklog.md)의 "batch 1의 예측"에 있다. 원자료는 `results/2026-11-18-p1-rttc`, `results/2026-11-18-p1-loss`, `results/2026-11-18-p1-rate`에 있다.

## 1. 배경과 방법

[results-phase1-pilot.md](results-phase1-pilot.md)는 서버의 인증서 서명에 지연을 주입해 handshake가 그 지연을 어떻게 견디는지를 RTT_c 20 ms, loss 없음, 동시 handshake 1~100개에서 측정했다. 이 문서는 같은 방법으로 설계의 나머지 변인을 채운다. RTT_c 1 ms와 100 ms, client link의 loss, 정해진 rate로 들어오는 handshake다.

서버는 quic-go, aioquic, quiche이고 client는 quic-go다. 서명은 서버 process 안에서 하며, 서명하기 전에 정해진 시간을 기다린다. 이 문서에서 "지연"은 그 주입한 시간이다. 서버에는 CPU 2개를 주었다. client의 ClientHello는 key share가 커서 Initial packet 2개에 나뉘어 나간다.

## 2. 요약

1. RTT_c 1 ms와 100 ms에서도 T_hs 중앙값은 "지연이 없을 때의 값 + 지연"과 0.81 ms 안에서 같았다. 지연은 0~1,200 ms이고 세 서버 모두에서 그랬다. 가설 H1의 선형 관계는 RTT_c와 무관하게 성립한다.
2. client link에 loss가 있을 때의 꼬리는 서버가 client의 Initial을 언제 확인해 주는지에 달렸다. quiche와 aioquic은 client의 첫 Initial을 받자마자 ACK를 담은 packet을 보내 client가 RTT 표본을 얻는다. quic-go 서버의 ServerHello가 손실되면 client는 표본 없이 200 ms를 기다렸다. RTT_c 1 ms에서 150 ms 넘게 늦어진 handshake는 quic-go가 400개 중 10~11개, quiche가 0~1개였다.
3. aioquic은 서명이 끝난 뒤에 보낸 packet이 손실되면 300 ms 뒤에 다시 보냈다. 지연이 100 ms일 때 250 ms 넘게 늦어진 handshake가 RTT_c 1 ms와 20 ms에서 400개 중 14개와 8개였다.
4. RTT_c 100 ms에서는 세 서버의 꼬리가 같았다. RTT 표본이 있을 때의 probe timeout(300 ms)이 표본이 없을 때의 값(200 ms)보다 길기 때문이다.
5. 정해진 rate로 handshake가 들어올 때 quic-go는 초당 1,600개까지 지연과 무관하게 T_hs를 유지했다. quiche는 초당 1,600개를 처리했지만 T_hs 중앙값이 2.5~3.3 ms 늘었다. aioquic의 처리율은 "1 / (지연 + 1.6 ms)"를 따랐다. 지연 0, 5, 20 ms에서 초당 약 640, 157, 47개다.

## 3. RTT_c 1 ms와 100 ms

handshake를 하나씩 했고 조건마다 100회다. 54개 조건 모두에서 100회가 전부 성공했다. 각 칸은 T_hs 중앙값(ms)이다.

| 서버 | RTT_c | 지연 0 | 5 | 50 | 200 | 1,200 ms |
| --- | --- | --- | --- | --- | --- | --- |
| quic-go | 1 ms | 1.97 | 7.44 | 52.46 | 202.23 | 1,202.66 |
| aioquic | 1 ms | 2.40 | 7.59 | 52.72 | 202.78 | 1,203.10 |
| quiche | 1 ms | 1.74 | 6.92 | 52.31 | 202.48 | 1,202.41 |
| quic-go | 100 ms | 101.01 | 106.26 | 151.43 | 301.26 | 1,301.65 |
| aioquic | 100 ms | 101.56 | 106.69 | 151.71 | 301.88 | 1,302.11 |
| quiche | 100 ms | 100.89 | 105.82 | 150.95 | 301.26 | 1,301.61 |

지연 9가지(0, 1, 5, 20, 50, 100, 200, 500, 1,200 ms) 전체에서 "T_hs 중앙값 − 지연이 없을 때의 중앙값 − 지연"은 −0.17~0.81 ms였다. p99와 중앙값의 차이는 가장 큰 조건에서 2.8 ms였다.

client가 handshake를 마칠 때까지 보낸 packet 수는 서버와 RTT_c에 따라 달랐다. 아래는 평균이다.

| 서버 | RTT_c | 지연 0 | 100 | 1,200 ms |
| --- | --- | --- | --- | --- |
| quic-go | 1 ms | 2.1 | 3.0 | 7.3 |
| aioquic | 1 ms | 3.0 | 10.9 | 18.5 |
| quiche | 1 ms | 2.0 | 7.9 | 11.9 |
| quic-go | 100 ms | 2.0 | 3.8 | 6.1 |
| aioquic | 100 ms | 3.0 | 3.0 | 7.0 |
| quiche | 100 ms | 2.0 | 3.0 | 5.0 |

aioquic과 quiche는 client의 Initial을 곧바로 확인해 주므로 client의 RTT 표본이 RTT_c와 같다. RTT_c가 1 ms이면 client의 probe timeout이 수 ms가 되어, 서명을 기다리는 동안 client가 Initial을 여러 번 다시 보낸다. T_hs는 늘지 않았지만 지연 1,200 ms에서 client가 보낸 packet이 12~18개였다. RTT_c 100 ms에서는 probe timeout이 길어 다시 보내는 횟수가 줄었다.

## 4. client link의 loss

client link에 방향마다 loss 2%를 주고 handshake를 하나씩 400회 했다. 27개 조건 모두에서 400회가 전부 성공했다. 각 칸은 T_hs의 중앙값 / p95 / p99(ms)다.

| RTT_c | 지연 | quic-go | aioquic | quiche |
| --- | --- | --- | --- | --- |
| 1 ms | 0 | 1.9 / 6.1 / 202.3 | 2.4 / 6.2 / 7.4 | 1.7 / 5.8 / 10.1 |
| 1 ms | 20 ms | 22.1 / 26.2 / 222.9 | 22.7 / 25.5 / 86.1 | 22.4 / 23.0 / 26.4 |
| 1 ms | 100 ms | 102.3 / 106.6 / 302.8 | 102.8 / 106.9 / 406.5 | 102.2 / 104.7 / 106.6 |
| 20 ms | 0 | 21.0 / 44.3 / 82.6 | 21.6 / 24.1 / 82.8 | 20.9 / 82.0 / 102.3 |
| 20 ms | 20 ms | 41.2 / 102.6 / 241.9 | 41.7 / 64.8 / 104.6 | 41.0 / 102.1 / 104.6 |
| 20 ms | 100 ms | 121.4 / 182.3 / 321.8 | 121.8 / 182.8 / 425.0 | 121.5 / 122.9 / 182.7 |
| 100 ms | 0 | 101.0 / 301.1 / 402.4 | 101.6 / 402.7 / 403.7 | 100.9 / 401.4 / 502.1 |
| 100 ms | 20 ms | 121.2 / 321.5 / 423.8 | 121.7 / 124.3 / 423.4 | 120.9 / 401.8 / 423.0 |
| 100 ms | 100 ms | 201.1 / 314.6 / 502.6 | 201.8 / 206.8 / 504.8 | 201.0 / 401.9 / 502.8 |

중앙값은 loss와 무관했다. 꼬리는 늦어진 크기에 따라 묶였다. 아래 표는 중앙값보다 150 ms 넘게 늦어진 handshake의 수(400개 중)다.

| RTT_c | 지연 | quic-go | aioquic | quiche |
| --- | --- | --- | --- | --- |
| 1 ms | 0 | 10 | 1 | 1 |
| 1 ms | 20 ms | 11 | 2 | 0 |
| 1 ms | 100 ms | 11 | 15 | 0 |
| 20 ms | 0 | 4 | 0 | 0 |
| 20 ms | 20 ms | 13 | 0 | 0 |
| 20 ms | 100 ms | 8 | 8 | 0 |
| 100 ms | 0 | 21 | 23 | 26 |
| 100 ms | 20 ms | 22 | 14 | 32 |
| 100 ms | 100 ms | 16 | 13 | 34 |

늦어진 handshake의 client qlog를 조건 6개에서 열어 원인을 확인했다.

- quic-go 서버에서 200 ms 늦어진 handshake는 대부분 서버의 첫 Initial(ServerHello)이 손실된 경우였다. RTT_c 1 ms, 지연 0의 10개 중 9개, RTT_c 20 ms, 지연 20 ms의 13개 중 10개다. client는 Handshake packet을 받았지만 key가 없어 쌓아 두었고, RTT 표본이 없어 처음의 probe timeout 200 ms를 기다린 뒤 Initial을 다시 보냈다.
- quiche와 aioquic은 client의 첫 Initial을 받자마자 ACK를 담은 작은 packet을 먼저 보냈다. client는 이 packet으로 RTT 표본을 얻는다. 그 뒤의 packet이 손실되어도 client의 probe timeout이 RTT_c의 3배쯤이어서 RTT_c 1 ms에서는 3~17 ms, 20 ms에서는 약 61 ms 뒤에 복구됐다. quiche는 ServerHello도 서명 전에 보내고, aioquic은 ServerHello를 서명이 끝난 뒤에 보낸다.
- quic-go 서버도 client의 Initial 2개 중 하나만 받으면 받은 것을 ACK로 확인해 준다. 이 경우 client가 표본을 얻어 61 ms 뒤에 복구했다(RTT_c 20 ms, 지연 20 ms에서 21개).
- aioquic에서 300 ms 늦어진 handshake는 서명이 끝난 뒤 서버가 보낸 Initial이 손실된 경우였다. client는 서명을 기다리는 동안 보낸 Initial이 모두 확인된 상태여서 다시 보낼 것이 없었고, 서버가 302 ms 뒤에 다시 보냈다. 지연이 없으면 client의 probe timeout이 먼저 나가므로 이 일이 생기지 않았다.
- quiche에서는 서명이 끝난 뒤의 packet이 손실되면 client가 probe timeout에 PING을 보냈고 서버가 곧바로 다시 보냈다.

RTT_c 100 ms에서는 세 서버 모두 300 ms 또는 400 ms 늦어졌다. 표본이 있을 때의 probe timeout이 약 300 ms여서 표본이 없을 때의 200 ms보다 오히려 길다. 이 RTT_c에서는 먼저 확인해 주는 것이 꼬리를 줄이지 못했다. quiche에서 늦어진 handshake가 26~34개로 다른 서버(13~23개)보다 많았는데, 원인은 확인하지 않았다.

## 5. 정해진 rate의 handshake

client가 정해진 rate로 handshake를 10초 동안 시작했다. 시작 시각은 앞선 handshake의 완료와 무관하다. RTT_c는 20 ms이고 loss는 없다. "처리율"은 그 10초 안에 완료된 handshake를 10으로 나눈 값이다. 각 칸은 처리율(초당) / T_hs 중앙값(ms)이다.

| 서버 | 지연 | rate 50 | 100 | 200 | 400 | 800 | 1,600 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| quic-go | 0 | 50 / 20.7 | 100 / 20.6 | 200 / 20.6 | 399 / 20.6 | 798 / 20.5 | 1,597 / 20.6 |
| quic-go | 5 ms | 50 / 25.8 | 100 / 25.8 | 200 / 25.8 | 399 / 25.8 | 798 / 25.7 | 1,596 / 25.8 |
| quic-go | 20 ms | 50 / 40.9 | 100 / 40.8 | 199 / 40.7 | 398 / 40.7 | 797 / 40.6 | 1,594 / 40.7 |
| quiche | 0 | 50 / 20.6 | 100 / 20.6 | 200 / 20.6 | 399 / 20.7 | 797 / 21.1 | 1,592 / 23.1 |
| quiche | 5 ms | 50 / 25.9 | 100 / 25.9 | 200 / 25.6 | 399 / 25.9 | 796 / 26.2 | 1,593 / 29.0 |
| quiche | 20 ms | 50 / 40.6 | 100 / 40.6 | 199 / 40.6 | 398 / 40.8 | 794 / 41.5 | 1,582 / 43.9 |
| aioquic | 0 | 50 / 21.2 | 100 / 21.1 | 200 / 21.1 | 399 / 21.1 | 662 / 59.0 | 623 / 2,043 |
| aioquic | 5 ms | 50 / 26.3 | 100 / 26.3 | 161 / 372.6 | 158 / 6,371 | 156 / 12,776 | 155 / 12,811 |
| aioquic | 20 ms | 47 / 328.8 | 47 / 3,552 | 47 / 13,152 | 47 / 13,294 | 47 / 13,403 | 47 / 13,407 |

- quic-go는 모든 조건에서 들어온 handshake를 그대로 처리했고 T_hs가 rate에 따라 변하지 않았다. 서명을 기다리는 handshake가 서로를 막지 않는다.
- quiche도 초당 1,600개를 처리했다. 다만 T_hs 중앙값이 rate 1,600에서 2.5~3.3 ms 늘었고, 마지막 2초에 시작한 handshake의 중앙값은 rate 50의 중앙값보다 7.7~10.2 ms 길었다. 지연 20 ms, rate 1,600의 p99는 105.4 ms다. 포화에 가까운 상태로 본다. 더 높은 rate는 측정하지 않았다.
- aioquic은 처리율에 상한이 있었다. 지연 0에서 초당 623~662개, 5 ms에서 155~161개, 20 ms에서 47개다. 서명을 기다리는 동안 다른 handshake를 처리하지 않으므로 상한이 "1 / (지연 + handshake 하나의 처리 시간)"이 된다. 지연 0의 상한에서 처리 시간을 1.5~1.6 ms로 잡으면 5 ms에서 152~154개, 20 ms에서 46개가 나오며 측정과 맞는다.
- aioquic에서 상한을 넘는 rate로 들어온 handshake는 쌓였다. 지연 0의 rate 1,600, 지연 5 ms의 rate 400 이상, 지연 20 ms의 rate 100 이상에서는 handshake의 일부가 client의 제한 시간 안에 끝나지 못했다. 지연 20 ms, rate 1,600에서 성공한 것은 16,000개 중 1,600개다.

## 6. 예측과 판정

| 예측 | 결과 | 판정 |
| --- | --- | --- |
| RTT_c 1 ms와 100 ms에서도 T_hs는 주입한 지연만큼만 늘어난다. | 차이 −0.17~0.81 ms | 맞음 |
| client link의 loss에서 quic-go 서버는 서명 지연이 클수록 꼬리가 더 길어지고, 먼저 응답하는 quiche에서는 그 증가가 작다. | RTT_c 1 ms와 20 ms에서 quiche는 200 ms 넘게 늦어진 handshake가 0~1개였고 quic-go는 4~13개였다. 다만 quic-go의 200 ms는 지연이 0일 때도 나타났다. 원인은 서명 지연이 아니라 ServerHello가 손실될 때 client에 RTT 표본이 없는 것이다. | 일부 맞음 |
| aioquic은 "1 / (지연 + handshake 처리 시간)"에서 포화한다. 지연 5 ms에서 초당 약 110개, 20 ms에서 약 45개다. | 식은 맞았다. 5 ms에서 155~161개, 20 ms에서 47개였다. 처리 시간을 4 ms로 잡은 것이 틀렸고 실제는 약 1.6 ms였다. | 일부 맞음 |
| quic-go와 quiche의 포화점은 서명 지연과 무관하다. | 두 서버 모두 초당 1,600개까지 포화하지 않았다. quiche는 지연이 클수록 T_hs의 증가가 조금 컸다. | 측정 범위에서 맞음 |

## 7. 한계

- loss 조건은 400회씩이다. 늦어진 handshake는 조건마다 0~34개이므로 수의 차이가 작은 조건끼리는 비교할 수 없다.
- qlog로 원인을 확인한 것은 조건 6개다. RTT_c 100 ms에서 quiche가 더 자주 늦어진 원인은 확인하지 않았다.
- loss는 서로 독립인 손실이다. 연달아 일어나는 손실은 측정하지 않았다.
- client는 quic-go 하나다. 꼬리의 크기(200 ms, RTT_c의 3배)는 이 client의 probe timeout 계산에서 나온 값이다.
- rate 측정은 10초씩이며 초당 1,600개가 상한이다. quic-go와 quiche의 포화점은 측정하지 못했다.
- 서버는 CPU 2개를 썼고, host에서 실험과 무관한 process가 CPU를 쓰고 있었다. 측정 중 1분 load average는 1.5~6.7이었다.

## 8. 설계에 주는 영향

- 서명 지연 D_sign이 T_hs에 그대로 더해진다는 모델은 RTT_c 1~100 ms에서 쓸 수 있다.
- loss가 있는 client link에서는 서버가 client의 Initial을 먼저 확인해 주는지가 꼬리를 정한다. RTT_c가 작을수록 차이가 크다. RTT_c 1 ms에서 quic-go 서버는 handshake의 2.5~2.8%가 200 ms 늦어졌고 quiche는 그런 handshake가 400개 중 0~1개였다. threshold signing을 붙이는 서버는 서명을 기다리는 동안이 아니라 ClientHello를 받은 직후에 ACK를 보내는 것이 좋다.
- aioquic처럼 서명을 기다리는 동안 다른 handshake를 처리하지 못하는 서버에서는 D_sign이 곧 처리율의 상한이다. D_sign 20 ms이면 초당 47개다. 이런 서버에 threshold signing을 붙이려면 서명을 event loop 밖으로 빼야 한다.
