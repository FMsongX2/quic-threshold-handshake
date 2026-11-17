# Phase 2 결과: 네트워크와 시스템 환경

측정일은 2026년 11월 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 측정 전의 예측은 [worklog.md](worklog.md)의 "batch 1의 예측"에 있다. 원자료는 `results/2026-11-18-network`, `results/2026-11-18-network-loss`, `results/2026-11-18-system`, `results/2026-11-18-signer-capacity`에 있다.

## 1. 배경과 방법

[results-phase2-signer.md](results-phase2-signer.md)는 실제 signer를 붙인 handshake를 RTT_c 20 ms, front server의 CPU 2개에서만 측정했다. 이 문서는 [experiment-design.md](experiment-design.md)의 네트워크 변인과 시스템 변인을 채운다. RTT_c와 RTT_s의 조합, client link와 signer link의 loss, front server의 CPU 수, 동시 handshake 수, signer의 처리율이다.

공통 조건은 다음과 같다. 서버는 quic-go이고 인증서 key는 process 안의 coordinator다. coordinator와 signer 사이는 signer당 유지되는 QUIC 연결 4개이고 정책은 fastest다. signer의 RTT_s는 모든 signer에 같은 값을 주었다. signer는 CPU 하나씩을 쓴다. scheme의 표기에서 (1, 1)은 key 전체를 원격 signer 하나가 갖는 비교 기준이다.

## 2. 요약

1. handshake를 하나씩 할 때 T_hs 중앙값은 RTT_c, RTT_s, scheme의 72개 조합 모두에서 "RTT_c + D_sign + 약 1 ms"였다. T_hs에서 D_sign을 뺀 값은 RTT_c 1, 20, 100 ms에서 1.9~2.3, 20.8~21.1, 100.9~101.2 ms다. 서명 지연과 client 쪽 왕복은 서로 영향을 주지 않고 더해진다.
2. signer link의 loss는 D_sign의 꼬리를 그대로 T_hs의 꼬리로 옮겼다. 손실이 난 서명은 "2 × RTT_s + 27 ms"가 걸렸고, 영향을 받는 서명의 비율은 signer 수와 round 수에 따라 4%에서 21%로 늘었다.
3. client link의 loss 1%는 T_hs의 p99를 57~82 ms 또는 약 200 ms 늘렸고, p95는 조건 8개 중 7개에서 4 ms 넘게 바꾸지 않았다.
4. front server의 CPU 수는 handshake가 몰릴 때만 드러났다. 동시 handshake 100개에서 T_hs에서 D_sign을 뺀 값은 CPU 1, 2, 4개에서 28.0~31.3, 24.5~25.9, 22.6~23.8 ms였다. 하나씩 할 때는 CPU 수와 무관하게 21.0 ms였다.
5. handshake가 몰릴 때 T_hs를 정한 것은 signer의 계산 대기였다. threshold RSA (3, 5)의 D_sign 중앙값은 동시 1, 10, 100개에서 9.6, 45.1, 375.4 ms였다. signer 하나가 share를 하나씩 계산하기 때문이다.
6. signer의 처리율 상한은 share 계산 시간의 역수와 맞았다. threshold RSA signer는 CPU 하나로 초당 200개를 처리했고 250개에서 밀리기 시작했다. CPU 2개로는 300개를 처리했고 400개에서 밀렸다. FROST signer는 초당 1,600개에서도 밀리지 않았다.

## 3. RTT_c와 RTT_s

handshake를 하나씩 순서대로 했고 조건마다 200회다. 72개 조건 모두에서 200회가 전부 성공했다. 아래 표는 RTT_c 20 ms의 T_hs 중앙값이고 괄호 안은 D_sign 중앙값이다(ms).

| scheme | (t, n) | RTT_s 0 ms | 5 ms | 50 ms |
| --- | --- | --- | --- | --- |
| Ed25519, 원격 signer 하나 | (1, 1) | 21.12 (0.17) | 26.23 (5.21) | 71.21 (50.20) |
| precomputation FROST | (2, 3) | 21.45 (0.41) | 26.43 (5.41) | 71.43 (50.39) |
| precomputation FROST | (3, 5) | 21.50 (0.51) | 26.60 (5.57) | 71.63 (50.56) |
| FROST | (2, 3) | 21.51 (0.53) | 31.66 (10.63) | 121.65 (100.62) |
| FROST | (3, 5) | 21.78 (0.72) | 31.82 (10.79) | 121.89 (100.80) |
| RSA, 원격 signer 하나 | (1, 1) | 21.64 (0.68) | 26.70 (5.70) | 71.71 (50.72) |
| threshold RSA | (2, 3) | 25.87 (5.13) | 31.08 (10.18) | 76.11 (55.17) |
| threshold RSA | (3, 5) | 27.89 (7.08) | 33.17 (12.15) | 76.25 (55.23) |

T_hs 중앙값에서 D_sign 중앙값을 뺀 값을 RTT_c별로 모으면 다음과 같다. 범위는 scheme 8가지와 RTT_s 3가지의 24개 조건에 걸친 값이다.

| RTT_c | T_hs − D_sign의 범위 | RTT_c를 뺀 값 |
| --- | --- | --- |
| 1 ms | 1.85~2.27 ms | 0.85~1.27 ms |
| 20 ms | 20.75~21.09 ms | 0.75~1.09 ms |
| 100 ms | 100.92~101.16 ms | 0.92~1.16 ms |

RTT_c가 100배 달라져도 서명 지연이 T_hs에 더하는 양은 D_sign 그대로였다. RTT_c 100 ms, FROST (3, 5), RTT_s 50 ms 조건의 T_hs는 201.82 ms이고 D_sign은 100.84 ms다. 이 조건의 D_sign은 quic-go client가 RTT 표본을 얻기 전에 쓰는 probe timeout(200 ms)보다 짧다. D_sign이 probe timeout보다 긴 조건은 서명 지연을 주입한 측정에서 다룬다.

## 4. loss

RTT_c는 20 ms다. handshake를 하나씩 했고 조건마다 400회이며 전부 성공했다. loss는 방향마다 같은 비율로 주었다. 각 칸은 T_hs의 중앙값 / p95 / p99(ms)다. loss가 없는 열은 3절의 측정(조건마다 200회)에서 가져왔다.

### 4.1 signer link의 loss

| scheme | RTT_s | loss 0% | loss 1% | loss 2% |
| --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 26.2 / 26.7 / 27.2 | 26.2 / 27.2 / 59.4 | 26.2 / 27.2 / 58.6 |
| Ed25519 (1, 1) | 50 ms | 71.2 / 71.7 / 72.7 | 71.2 / 71.8 / 151.3 | 71.2 / 72.6 / 150.2 |
| precomputation FROST (3, 5) | 5 ms | 26.6 / 27.7 / 28.9 | 26.6 / 58.6 / 67.7 | 26.7 / 60.0 / 84.0 |
| precomputation FROST (3, 5) | 50 ms | 71.6 / 72.7 / 76.7 | 71.6 / 148.8 / 150.4 | 71.7 / 153.9 / 223.4 |
| FROST (3, 5) | 5 ms | 31.8 / 32.8 / 33.1 | 31.8 / 65.6 / 79.1 | 31.9 / 71.2 / 103.6 |
| FROST (3, 5) | 50 ms | 121.9 / 123.6 / 124.5 | 121.9 / 199.1 / 227.2 | 122.0 / 231.5 / 308.1 |
| threshold RSA (3, 5) | 5 ms | 33.2 / 33.7 / 34.0 | 33.2 / 35.1 / 70.4 | 31.3 / 70.9 / 116.2 |
| threshold RSA (3, 5) | 50 ms | 76.3 / 77.7 / 79.3 | 76.3 / 153.2 / 200.0 | 76.3 / 161.5 / 210.9 |

중앙값은 loss와 무관했다. 꼬리의 크기는 손실 하나가 더하는 시간(RTT_s + 27 ms)으로 설명된다. signer 하나를 쓰는 구성에서 p99는 RTT_s 5 ms일 때 약 32 ms, 50 ms일 때 약 78 ms 늘었다. 꼬리가 시작되는 백분위는 요청과 응답이 오가는 횟수에 달렸다. 방향별 loss율을 p라 하면 서명이 손실을 겪을 확률은 1 − (1 − p)^(2 × round 수 × signer 수)다. 이 값은 signer 하나에 round 하나이면 loss 1%에서 2%, loss 2%에서 4%다. signer 셋에 round 하나이면 6%와 11%, signer 셋에 round 둘이면 11%와 21%다. 그래서 signer 하나를 쓰는 구성은 p95가 그대로였고, signer 셋을 쓰는 구성은 loss 2%에서 모두 p95가 늘었다. loss 1%에서는 확률 6%가 p95의 경계에 있어, threshold RSA의 RTT_s 5 ms 조건만 p95가 35.1 ms로 남았다.

### 4.2 client link의 loss

client link에 loss 1%를 주었다. "둘 다"는 signer link에도 1%를 준 조건이다.

| scheme | RTT_s | loss 없음 | client link | 둘 다 |
| --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 5 ms | 26.2 / 26.7 / 27.2 | 26.2 / 26.9 / 87.5 | 26.2 / 58.2 / 87.6 |
| Ed25519 (1, 1) | 50 ms | 71.2 / 71.7 / 72.7 | 71.2 / 73.3 / 271.7 | 71.2 / 132.6 / 271.7 |
| precomputation FROST (3, 5) | 5 ms | 26.6 / 27.7 / 28.9 | 26.6 / 31.0 / 88.5 | 26.6 / 58.9 / 88.2 |
| precomputation FROST (3, 5) | 50 ms | 71.6 / 72.7 / 76.7 | 71.6 / 76.5 / 133.3 | 71.6 / 153.0 / 272.1 |
| FROST (3, 5) | 5 ms | 31.8 / 32.8 / 33.1 | 31.9 / 34.1 / 232.7 | 31.9 / 67.4 / 93.4 |
| FROST (3, 5) | 50 ms | 121.9 / 123.6 / 124.5 | 121.8 / 145.2 / 322.4 | 121.9 / 200.9 / 322.3 |
| threshold RSA (3, 5) | 5 ms | 33.2 / 33.7 / 34.0 | 33.1 / 35.1 / 95.0 | 33.1 / 65.4 / 94.9 |
| threshold RSA (3, 5) | 50 ms | 76.3 / 77.7 / 79.3 | 76.2 / 78.2 / 160.8 | 75.9 / 153.7 / 276.5 |

client link의 loss 1%에서 p99는 loss가 없을 때보다 57~82 ms 또는 약 200 ms 길었다. 조건마다 400회이므로 p99는 표본 4개 안팎으로 정해진다. 60 ms는 RTT_c의 3배와 같고 200 ms는 quic-go client가 RTT 표본 없이 기다리는 probe timeout과 같다. 어떤 packet이 손실됐는지는 이 측정에서 확인하지 않았다. 서버 구현체가 서명을 기다리는 동안 client에 무엇을 보내는지에 따라 이 꼬리가 달라지는지는 서명 지연을 주입한 측정에서 구현체별로 비교한다.

두 link에 모두 loss가 있으면 손실을 겪는 handshake의 비율이 더해진다. 원격 signer 하나를 쓰는 구성은 한쪽에만 loss가 있을 때 p95가 그대로였고, 둘 다 있으면 p95가 58.2 ms와 132.6 ms로 늘었다. p99는 표본 4개 안팎으로 정해지는 값이어서, 한쪽에만 loss를 준 조건과의 대소가 일정하지 않았다.

## 5. front server의 CPU와 동시 handshake 수

RTT_c는 20 ms, RTT_s는 5 ms다. front server에 logical CPU 1, 2, 4개를 주었다. CPU 4개는 물리 core 2개의 thread 4개다. 동시 handshake 1, 10, 100개에서 조건마다 200, 1,000, 5,000개를 측정했고 전부 성공했다.

아래 표는 T_hs 중앙값이고 괄호 안은 D_sign 중앙값이다(ms).

| scheme | CPU | 동시 1개 | 10개 | 100개 |
| --- | --- | --- | --- | --- |
| Ed25519 (1, 1) | 1 | 26.2 (5.2) | 26.6 (5.1) | 39.8 (10.7) |
| Ed25519 (1, 1) | 2 | 26.2 (5.2) | 26.5 (5.1) | 31.7 (5.7) |
| Ed25519 (1, 1) | 4 | 26.2 (5.2) | 26.5 (5.2) | 29.0 (5.3) |
| precomputation FROST (3, 5) | 1 | 26.7 (5.6) | 28.0 (6.3) | 49.2 (19.6) |
| precomputation FROST (3, 5) | 2 | 26.5 (5.5) | 27.7 (6.3) | 44.8 (19.4) |
| precomputation FROST (3, 5) | 4 | 26.6 (5.5) | 27.3 (6.0) | 44.3 (21.0) |
| FROST (3, 5) | 1 | 31.9 (10.9) | 33.1 (11.6) | 60.7 (31.9) |
| FROST (3, 5) | 2 | 31.9 (10.8) | 32.8 (11.4) | 51.7 (26.8) |
| FROST (3, 5) | 4 | 31.8 (10.8) | 32.6 (11.3) | 49.4 (26.7) |
| RSA (1, 1) | 1 | 26.8 (5.7) | 28.6 (7.3) | 53.0 (25.0) |
| RSA (1, 1) | 2 | 26.7 (5.7) | 28.7 (7.3) | 54.1 (29.6) |
| RSA (1, 1) | 4 | 26.7 (5.7) | 28.7 (7.6) | 54.6 (32.0) |
| threshold RSA (3, 5) | 1 | 31.2 (10.2) | 66.8 (45.4) | 408.7 (377.3) |
| threshold RSA (3, 5) | 2 | 30.7 (9.6) | 66.6 (45.1) | 400.4 (375.4) |
| threshold RSA (3, 5) | 4 | 33.2 (12.2) | 67.4 (46.1) | 410.5 (386.7) |

T_hs에서 D_sign을 뺀 값은 front server가 handshake 자체에 쓰는 시간이다. scheme 5가지에 걸친 범위는 다음과 같다.

| CPU | 동시 1개 | 10개 | 100개 |
| --- | --- | --- | --- |
| 1 | 21.0~21.1 ms | 21.3~21.6 ms | 28.0~31.3 ms |
| 2 | 21.0~21.1 ms | 21.3~21.6 ms | 24.5~25.9 ms |
| 4 | 21.0 ms | 21.1~21.3 ms | 22.6~23.8 ms |

- 동시 handshake가 10개 이하이면 CPU 수는 결과를 바꾸지 않았다.
- 동시 100개에서 RTT_c와 1 ms를 뺀 나머지는 CPU 1개일 때 7.0~10.3 ms, 2개일 때 3.5~4.9 ms다. CPU가 절반이면 대기가 약 2배가 됐다.
- D_sign은 front server의 CPU 수와 거의 무관하게 동시 handshake 수에 따라 늘었다. 이 측정은 동시 handshake 수 N을 고정하므로, signer가 요청 하나에 s를 쓰면 handshake 하나가 한 바퀴 도는 시간은 N × s보다 짧을 수 없다. threshold RSA의 share 계산은 4.0~5.0 ms이므로 N이 100이면 400~500 ms이고, 측정한 T_hs는 400.4~410.5 ms다. RSA 서명은 0.55 ms이므로 55 ms이고, 측정한 T_hs는 53.0~54.6 ms다. 두 구성의 T_hs는 signer의 계산이 정했다.
- FROST는 signer의 계산이 0.2~0.3 ms여서 같은 하한이 20~30 ms이고 T_hs(44.3~60.7 ms)보다 작다. FROST의 D_sign이 동시 100개에서 14~21 ms 늘어난 것에는 coordinator의 계산과 signer 연결의 대기가 함께 들어 있으며 따로 나누지 않았다.
- 원격 signer 하나를 쓰는 Ed25519는 계산이 0.02 ms 이하다. 이 구성에서는 front server의 CPU가 1개일 때만 D_sign이 10.7 ms로 늘었다. coordinator가 front server와 같은 process에서 CPU를 나누어 쓰기 때문으로 본다.

## 6. signer의 처리율

client가 정해진 rate로 handshake를 10초 동안 시작했다. 시작 시각은 앞선 handshake의 완료와 무관하다. RTT_c는 20 ms, RTT_s는 5 ms다. front server에는 CPU 4개를 주어 서버가 먼저 밀리지 않게 했다. "마지막 2초"는 8초 이후에 시작한 handshake의 T_hs 중앙값이다. 처리율을 넘으면 요청이 쌓여 이 값이 처음보다 커진다.

| scheme | signer의 CPU | rate (초당) | 성공 | T_hs 중앙값 | 처음 2초 | 마지막 2초 |
| --- | --- | --- | --- | --- | --- | --- |
| threshold RSA (2, 3) | 1 | 100 | 1,000 / 1,000 | 30.7 ms | 30.8 ms | 30.7 ms |
| threshold RSA (2, 3) | 1 | 200 | 2,000 / 2,000 | 31.7 ms | 31.0 ms | 31.8 ms |
| threshold RSA (2, 3) | 1 | 250 | 2,500 / 2,500 | 304.2 ms | 30.8 ms | 1,225.1 ms |
| threshold RSA (2, 3) | 1 | 300 | 3,000 / 3,000 | 921.0 ms | 222.3 ms | 1,555.9 ms |
| threshold RSA (2, 3) | 1 | 400 | 3,904 / 4,000 | 12.9초 | 976.4 ms | 14.5초 |
| threshold RSA (2, 3) | 1 | 600 | 1,229 / 6,000 | 2.7초 | 2.1초 | 25.4초 |
| threshold RSA (2, 3) | 2 | 200 | 2,000 / 2,000 | 29.3 ms | 29.3 ms | 29.3 ms |
| threshold RSA (2, 3) | 2 | 300 | 3,000 / 3,000 | 30.9 ms | 30.8 ms | 31.3 ms |
| threshold RSA (2, 3) | 2 | 400 | 4,000 / 4,000 | 798.7 ms | 174.4 ms | 1,247.9 ms |
| threshold RSA (2, 3) | 2 | 600 | 5,900 / 6,000 | 15.0초 | 1,056.5 ms | 17.4초 |
| precomputation FROST (2, 3) | 1 | 400 | 4,000 / 4,000 | 25.9 ms | 25.9 ms | 25.9 ms |
| precomputation FROST (2, 3) | 1 | 1,600 | 16,000 / 16,000 | 26.0 ms | 26.0 ms | 26.1 ms |

- threshold RSA signer의 share 계산 시간 중앙값은 4.9 ms였다. 그 역수는 초당 204개다. CPU 하나인 signer는 초당 200개까지 T_hs를 유지했고 250개에서 밀렸다.
- CPU 2개인 signer는 300개까지 유지했고 400개에서 밀렸다. 2배에 못 미친다. 이 조건에서 share 계산 시간 중앙값은 rate 200에서 3.4 ms, 400에서 6.8 ms였다. signer 셋에 준 logical CPU 6개는 물리 core 4개에 걸쳐 있었고, 그중 하나는 실험과 무관한 process와 물리 core를 나누어 썼다. 부하가 높을 때 계산이 느려진 것은 이 때문으로 본다. 물리 core를 나누어 준 조건으로 확인하지는 않았다.
- 처리율을 넘는 rate에서는 T_hs가 시간에 따라 계속 늘었다. CPU 1개인 signer에서 초당 400개이면 handshake의 2.4%가, 600개이면 80%가 실패했다. 실패한 handshake는 서명 제한 시간(15초)이나 client의 제한 시간(30초)을 넘긴 것이다.
- FROST signer의 계산 시간은 0.11 ms여서 측정 범위에서 밀리지 않았다.

## 7. 예측과 판정

| 예측 | 결과 | 판정 |
| --- | --- | --- |
| handshake를 하나씩 할 때 T_hs − D_sign은 RTT_c마다 일정하고 RTT_c에 약 1 ms를 더한 값이다. | RTT_c + 0.75~1.27 ms | 맞음 |
| 동시 100개에서 T_hs − D_sign − RTT_c는 CPU 1개일 때 2개일 때의 약 2배다. | 7.0~10.3 ms 대 3.5~4.9 ms | 맞음 |
| threshold RSA의 share 계산은 약 4 ms이므로 signer는 CPU 하나로 초당 약 250개에서 포화한다. | share 계산은 4.9 ms였다. 200개는 유지했고 250개에서 밀렸다. | 대체로 맞음 |
| CPU 2개이면 포화점이 약 2배가 된다. | 300개는 유지, 400개에서 밀림 | 틀림 |
| FROST signer는 초당 1,600개에서 포화하지 않는다. | T_hs 26.0 ms 유지 | 맞음 |
| client link의 loss에서 T_hs의 p95는 D_sign이 클수록 더 많이 늘어난다. | p95의 증가는 D_sign이 100 ms인 조건 하나에서 21.6 ms였고 나머지 7개에서 0.2~3.8 ms였다. loss 1%와 400회로는 손실이 p95에 닿는 조건이 적어 가릴 수 없었다. | 판정 보류 |

## 8. 한계

- 서버는 quic-go 하나, client도 quic-go 하나다. 구현체별 비교는 별도 측정에서 다룬다.
- loss 조건은 400회씩이어서 p99의 표본이 4개 안팎이다. p99 값은 크기의 정도로만 읽어야 한다.
- front server의 "CPU 4개"는 물리 core 2개의 thread 4개다. 물리 core 4개인 조건은 host에 여유가 없어 측정하지 못했다.
- signer에 CPU 2개를 준 조건에서 signer들의 CPU가 물리 core를 공유했다. 처리율이 2배가 되지 않은 원인을 물리 core를 나눈 조건으로 확인하지 않았다.
- signer 복제본을 두어 처리율을 늘리는 구성은 측정하지 않았다.
- host에서 실험과 무관한 process가 CPU를 쓰고 있었다. 측정 중 1분 load average는 1.4~4.8이었다.

## 9. 설계에 주는 영향

- 지연 모델의 덧셈 구조(T_hs = RTT_c에 따른 기본값 + D_sign)는 RTT_c 1~100 ms와 RTT_s 0~50 ms에서 유지된다. 서명 구성의 최적화는 D_sign만 보고 해도 된다.
- 모델에 넣을 시스템 항은 둘이다. front server의 handshake 처리 대기는 동시 100개, CPU 2개에서 약 4 ms다. signer의 계산은 동시 handshake 수 N에서 T_hs의 하한 "N × 요청당 계산 시간"을 만들며, threshold RSA에서는 이 하한이 네트워크 항보다 훨씬 크다.
- threshold RSA를 쓰는 구성은 signer의 처리율(이 host에서 CPU당 초당 약 200개)이 handshake rate의 상한이 된다. FROST는 그 상한이 측정 범위 밖에 있다.
