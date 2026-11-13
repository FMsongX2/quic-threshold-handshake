# Phase 3 결과: FROST hedging

측정일은 2026년 11월 13일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 구현 결정은 [worklog.md](worklog.md)에 있다. 원자료는 `results/2026-11-13-phase3-hedge-*`에 있다.

## 1. 배경과 방법

Phase 2에서 signer 링크에 loss가 있으면, 선택한 signer 전부의 응답을 기다리는 구성은 서명의 5~21%가 200 ms 안팎 늦어졌다([results-phase2-signer.md](results-phase2-signer.md) 5절). threshold RSA는 n개 전체에 요청해 먼저 온 t개의 share를 결합하는 first 정책으로 이를 피했다. FROST의 share는 한 세션의 commitment 목록에 묶여 있어 같은 방법을 쓸 수 없다.

hedge 정책은 FROST에서 같은 효과를 얻기 위한 방법이다.

- coordinator는 RTT 추정이 가장 짧은 signer m개를 고른다(t ≤ m ≤ n).
- m개에서 뽑을 수 있는 t개 조합마다 FROST 세션을 하나씩 진행한다. 세션마다 각 signer의 서로 다른 nonce commitment를 쓴다.
- signer 하나가 속한 세션들의 서명 요청은 요청 하나에 묶어 보낸다. 먼저 응답한 t개 signer가 한 세션을 완성하고, coordinator는 그 세션의 서명을 쓴다.
- precomputation을 쓰지 않으면, commitment 응답이 도착하는 대로 구성원 전원의 commitment가 모인 세션부터 서명 요청을 보낸다.

비용은 signer의 계산량과 nonce commitment 소비량이다. precomputation을 쓰면 서명 하나에 signer 하나가 share를 C(m−1, t−1)개 계산한다.

| t | m | 세션 수 | signer 하나가 계산하는 share 수 | 서명 하나의 전체 share 수 |
| --- | --- | --- | --- | --- |
| 2 | 2 | 1 | 1 | 2 |
| 2 | 3 | 3 | 2 | 6 |
| 2 | 4 | 6 | 3 | 12 |
| 2 | 5 | 10 | 4 | 20 |
| 3 | 3 | 1 | 1 | 3 |
| 3 | 4 | 4 | 3 | 12 |
| 3 | 5 | 10 | 6 | 30 |

이 문서의 정책 이름은 다음을 뜻한다.

| 이름 | 뜻 |
| --- | --- |
| fastest | RTT 추정이 가장 짧은 t개 signer로 세션 하나를 진행한다. m = t인 hedge와 같다. |
| hedge(m) | 위 방법을 signer m개로 진행한다. |
| first | threshold RSA에서 n개 전체에 요청해 먼저 온 t개의 share를 결합한다. |

RTT 추정은 Phase 2의 이동 평균에서 최근 8개 표본의 최솟값으로 바꿨다. 이 문서의 fastest 결과는 바꾼 뒤의 구현으로 측정했다.

측정 조건은 Phase 2와 같다. signer 5개의 RTT_s는 signer 번호 순서대로 50, 5, 100, 1, 20 ms이고, coordinator와 signer 사이 연결은 유지하는 방식이다. loss는 coordinator와 signer의 송신 방향에 각각 주었다. 측정 중 host의 1분 load average는 1.6~4.0이었다.

## 2. 요약

1. hedge는 signer 링크의 loss 2%에서 FROST의 꼬리 지연을 줄였다. precomputation을 쓰는 FROST (2, 5)에서 D_sign의 p95는 219.1 ms에서 20.4~20.8 ms로, (3, 5)에서 237.6 ms에서 50.9~51.7 ms로 줄었다. 중앙값의 차이는 1.3 ms 이하였다.
2. signer를 하나만 더 써도 효과의 대부분을 얻었다. 100 ms 넘게 늦어진 서명의 비율은 (2, 5)에서 9.6%(m = 2), 0.5%(m = 3), 0.0%(m = 4·5)였고, (3, 5)에서 12.7%(m = 3), 0.7%(m = 4), 0.0%(m = 5)였다. 이 비율은 "m개 중 loss 없이 응답한 signer가 t개 미만일 확률"로 예측한 값과 맞았다.
3. handshake에서도 같았다. quic-go 서버에 붙였을 때 T_hs의 p95는 precomputation FROST (2, 5)에서 231.4 ms에서 42.0 ms로, (3, 5)에서 264.8 ms에서 72.5 ms로 줄었다.
4. hedge는 loss가 없고 handshake가 몰릴 때 지연을 늘렸다. handshake 100개가 동시에 시작되는 조건에서 T_hs 중앙값은 precomputation FROST (3, 5)에서 50.7 ms(fastest), 77.8 ms(m = 4), 117.1 ms(m = 5)였다. 미리 받아 둔 nonce commitment가 모자라 hedge 구성에서는 서명의 38.5~79.4%가 commitment를 다시 받아 와야 했다.

## 3. 서명 지연

서명을 하나씩 순서대로 요청했고 조건마다 1,000회다. 40개 조건 모두에서 1,000회가 전부 성공했다. D_sign 열은 중앙값 / p95 / p99(ms)다. "느린 서명"은 loss 2%에서 D_sign이 같은 구성의 loss 없는 중앙값보다 100 ms 넘게 긴 서명의 비율이다.

### 3.1 precomputation을 쓰는 FROST

"share 수"는 서명 하나에 signer들이 계산한 share의 합이다. "예측"은 방향별 loss율 p = 0.02에서 signer 하나의 요청과 응답이 모두 손실 없이 도착할 확률 q = (1 − p)²로 계산한, m개 중 t개 미만이 손실 없이 응답할 확률이다.

| (t, n) | 정책 | share 수 | D_sign, loss 0% | D_sign, loss 2% | 느린 서명 | 예측 |
| --- | --- | --- | --- | --- | --- | --- |
| (2, 5) | fastest | 2 | 5.30 / 5.86 / 8.86 | 5.29 / 219.12 / 230.08 | 9.6% | 7.8% |
| (2, 5) | hedge(3) | 6 | 5.44 / 5.96 / 10.48 | 5.45 / 20.40 / 40.44 | 0.5% | 0.5% |
| (2, 5) | hedge(4) | 12 | 5.55 / 6.38 / 10.61 | 5.58 / 20.64 / 39.91 | 0.0% | 0.02% |
| (2, 5) | hedge(5) | 20 | 5.69 / 9.37 / 10.79 | 5.70 / 20.83 / 27.30 | 0.0% | 0.00% |
| (3, 5) | fastest | 3 | 20.36 / 20.59 / 23.30 | 20.39 / 237.56 / 257.07 | 12.7% | 11.4% |
| (3, 5) | hedge(4) | 12 | 20.87 / 22.23 / 27.39 | 20.89 / 50.93 / 100.91 | 0.7% | 0.9% |
| (3, 5) | hedge(5) | 30 | 21.62 / 28.03 / 28.15 | 21.64 / 51.70 / 68.72 | 0.0% | 0.06% |

loss 2%에서 hedge의 p95는 가장 가까운 signer 대신 그다음 signer의 응답을 기다린 시간이다. (2, 5)의 20 ms는 RTT_s가 20 ms인 signer, (3, 5)의 51 ms는 RTT_s가 50 ms인 signer에 해당한다. hedge(5)에서 RTT_s 20 ms인 signer는 1,000회 중 96회, 50 ms인 signer는 4회 쓰였다((2, 5) 기준).

loss가 없을 때 hedge는 중앙값을 0.1~1.3 ms, p95를 0.1~7.4 ms 늘렸다. hedge(5)·(3, 5)에서 p95가 28.0 ms로 늘어난 원인을 trace로 확인했다. 중앙값보다 3 ms 넘게 느린 서명이 1,000회 중 93회였고, 약 10.7회 간격으로 나타났다. coordinator는 commitment를 64개씩 보충하고 이 구성은 서명 하나에 signer마다 6개를 쓰므로 보충 주기가 10.7회다. fastest에서는 같은 현상이 64회 간격으로 나타났다. signer가 commitment 64개를 만드는 동안 서명 요청이 기다린 것으로 본다.

### 3.2 precomputation을 쓰지 않는 FROST

commit과 sign 두 round를 서명 요청마다 수행한다. "share 수"는 loss 2%에서 서명 하나에 계산한 share 수의 평균이다.

| (t, n) | 정책 | D_sign, loss 0% | D_sign, loss 2% | 느린 서명 | share 수 |
| --- | --- | --- | --- | --- | --- |
| (2, 5) | fastest | 10.50 / 10.97 / 12.50 | 10.51 / 224.85 / 263.10 | 13.2% | 2.00 |
| (2, 5) | hedge(3) | 10.54 / 11.09 / 12.09 | 10.59 / 40.54 / 221.36 | 1.7% | 2.33 |
| (2, 5) | hedge(4) | 10.63 / 11.88 / 12.52 | 10.67 / 40.73 / 90.71 | 0.3% | 2.29 |
| (2, 5) | hedge(5) | 10.70 / 11.00 / 12.69 | 10.72 / 40.82 / 60.86 | 0.3% | 2.34 |
| (3, 5) | fastest | 40.53 / 40.77 / 41.43 | 40.59 / 274.60 / 478.87 | 22.6% | 3.00 |
| (3, 5) | hedge(4) | 40.76 / 41.02 / 42.05 | 40.82 / 101.00 / 251.91 | 2.9% | 4.15 |
| (3, 5) | hedge(5) | 41.09 / 41.32 / 42.13 | 41.12 / 101.62 / 200.99 | 3.4% | 5.27 |

이 방식은 세션을 commitment가 모이는 순서대로 시작하므로, 가까운 t개 signer가 제때 응답하면 세션 하나만 진행된다. share 수는 loss가 없을 때 t개였고 loss 2%에서 t의 1.1~1.8배였다. 대신 signer는 서명마다 commitment를 C(m−1, t−1)개씩 만든다. 두 round 모두에서 다음 signer를 기다릴 수 있어 p95는 precomputation을 쓸 때의 두 배(40 ms, 101 ms)였다.

### 3.3 threshold RSA

| (t, n) | 정책 | share 수 | D_sign, loss 0% | D_sign, loss 2% | 느린 서명 |
| --- | --- | --- | --- | --- | --- |
| (2, 5) | fastest | 2 | 9.16 / 10.37 / 12.13 | 9.20 / 221.03 / 232.15 | 7.5% |
| (2, 5) | first | 5 | 10.29 / 10.49 / 11.00 | 10.24 / 27.14 / 27.42 | 0.0% |
| (3, 5) | fastest | 3 | 24.01 / 24.36 / 24.83 | 24.06 / 245.00 / 262.85 | 12.4% |
| (3, 5) | first | 5 | 27.09 / 27.33 / 27.76 | 27.09 / 57.22 / 104.28 | 0.2% |

RTT 추정을 최솟값으로 바꾼 뒤 fastest 정책은 loss 2%에서도 가장 가까운 t개 signer를 1,000회 모두 유지했다. Phase 2에서 본, 먼 signer에 머무는 현상은 나타나지 않았다.

## 4. handshake

quic-go 서버의 인증서 key를 coordinator로 두고 quic-go client로 측정했다. RTT_c는 20 ms이고 client와 서버 사이에는 loss를 주지 않았다. handshake를 하나씩 수행했고 조건마다 300회이며 모두 성공했다. T_hs 열은 중앙값 / p95 / p99(ms)다. "느린 handshake"는 loss 2%에서 T_hs가 그 조건의 중앙값보다 100 ms 넘게 긴 handshake의 수다.

| scheme (t, n) | 정책 | T_hs, signer loss 0% | T_hs, signer loss 2% | 느린 handshake |
| --- | --- | --- | --- | --- |
| frost-pre (2, 5) | fastest | 26.35 / 27.75 / 29.10 | 26.37 / 231.45 / 240.10 | 18 / 300 |
| frost-pre (2, 5) | hedge(5) | 26.79 / 30.03 / 31.69 | 26.85 / 42.04 / 68.42 | 0 / 300 |
| frost-pre (3, 5) | fastest | 41.48 / 42.25 / 44.47 | 41.75 / 264.80 / 281.79 | 41 / 300 |
| frost-pre (3, 5) | hedge(5) | 42.77 / 48.99 / 49.53 | 42.70 / 72.46 / 79.01 | 1 / 300 |
| rsa (2, 5) | fastest | 31.25 / 32.17 / 34.40 | 30.39 / 240.71 / 250.23 | 29 / 300 |
| rsa (2, 5) | first | 30.21 / 31.36 / 32.55 | 30.42 / 48.05 / 48.51 | 0 / 300 |
| rsa (3, 5) | fastest | 45.13 / 45.99 / 46.45 | 45.08 / 244.70 / 290.09 | 35 / 300 |
| rsa (3, 5) | first | 48.12 / 48.66 / 49.02 | 48.16 / 78.35 / 126.56 | 0 / 300 |

16개 조건 모두에서 T_hs 중앙값과 D_sign 중앙값의 차이는 20.97~21.24 ms였다. signer 링크의 loss가 만든 서명 지연은 그대로 client의 handshake 지연이 됐다.

## 5. handshake가 몰릴 때의 비용

client가 handshake 100개를 같은 시각에 시작하는 것을 5회 반복해 500개를 측정했다. loss는 없다. 모두 성공했다. "commitment 부족"은 미리 받아 둔 commitment가 모자라 서명 도중에 다시 받아 온 서명의 비율이다. 단위는 ms다.

| scheme (t, n) | 정책 | D_sign 중앙값 | T_hs 중앙값 / p95 / p99 | 서명당 share 수 | commitment 부족 |
| --- | --- | --- | --- | --- | --- |
| frost-pre (2, 5) | fastest | 9.50 | 34.23 / 45.97 / 49.87 | 2.00 | 0% |
| frost-pre (2, 5) | hedge(4) | 15.39 | 41.26 / 62.66 / 64.28 | 7.84 | 46.3% |
| frost-pre (2, 5) | hedge(5) | 18.97 | 45.60 / 67.41 / 69.32 | 8.68 | 77.6% |
| frost-pre (3, 5) | fastest | 25.60 | 50.71 / 70.37 / 74.36 | 3.00 | 0% |
| frost-pre (3, 5) | hedge(4) | 51.53 | 77.78 / 110.36 / 112.62 | 8.95 | 38.5% |
| frost-pre (3, 5) | hedge(5) | 89.08 | 117.11 / 146.83 / 154.05 | 12.99 | 79.4% |
| frost (3, 5) | fastest | 43.15 | 67.86 / 102.87 / 106.16 | 3.00 | |
| frost (3, 5) | hedge(4) | 53.12 | 78.03 / 98.10 / 102.53 | 3.86 | |
| frost (3, 5) | hedge(5) | 84.46 | 107.42 / 136.78 / 140.44 | 8.86 | |

precomputation을 쓰는 구성에서 coordinator는 signer마다 commitment를 최대 256개 보관한다. handshake 100개가 한꺼번에 오면 hedge(5)·(3, 5)는 signer마다 600개가 필요하다. commitment가 모자란 서명은 signer에게서 다시 받아 오므로 왕복이 한 번 더 들고, 그 요청도 signer에서 순서대로 처리된다. 이 구현은 FROST library 호출을 process 안에서 직렬화하므로([worklog.md](worklog.md)) signer가 계산할 share가 늘어난 만큼 대기도 늘어난다.

loss가 있으면서 handshake가 몰리는 조건은 측정하지 않았다.

## 6. 정리

같은 signer 배치에서 측정한 조건별로 지연이 가장 짧았던 FROST 구성은 다음과 같다. 모두 precomputation을 쓴다.

| 조건 | 기준 | (t, n) = (2, 5) | (t, n) = (3, 5) |
| --- | --- | --- | --- |
| loss 0%, 하나씩 | D_sign 중앙값 | fastest (5.30 ms) | fastest (20.36 ms) |
| loss 2%, 하나씩 | D_sign p99 | hedge(5) (27.30 ms) | hedge(5) (68.72 ms) |
| loss 0%, 동시 100개 | T_hs 중앙값 | fastest (34.23 ms) | fastest (50.71 ms) |

hedge의 폭 m은 loss에 대한 보호와 signer 부하 사이의 조절 값이다. m = t + 1은 느린 서명의 비율을 0.5~0.7%로 낮추면서 share 수를 fastest의 3~4배로 늘리고, m = n은 그 비율을 0.1% 미만으로 낮추면서 share 수를 10배로 늘린다.

threshold RSA의 first 정책은 share 수가 n개로 고정이고 signer가 share를 하나씩만 계산한다. FROST의 hedge는 세션마다 share가 따로 필요해 조합 수만큼 계산량이 늘어난다. 대신 FROST의 share 하나는 threshold RSA의 share 하나보다 계산 시간이 짧다(중앙값 0.1~0.2 ms 대 3.9~4.9 ms, [results-phase2-signer.md](results-phase2-signer.md) 3절).

## 7. 한계

- hedge 구성의 안전성을 따로 증명하거나 검토받지 않았다. 세션마다 서로 다른 nonce를 쓰며, 근거는 FROST가 동시 세션에서 안전하다는 성질이다.
- 이기지 못한 세션의 서명 요청이 signer에 도달하기 전에 취소되면 그 nonce가 signer의 memory에 남는다. 정리 절차를 넣지 않았다.
- signer 배치는 한 가지이고 RTT_s는 고정이다. 선택되지 않은 signer의 RTT 추정은 갱신하지 않으므로, RTT_s가 시간에 따라 변하는 조건에서 어느 signer를 m개에 넣을지는 다루지 않았다.
- loss는 packet마다 독립인 모형이다. 연속 손실이나 signer 장애는 측정하지 않았다. signer 하나가 응답하지 않아도 hedge가 서명을 완성하는 것은 단위 시험으로만 확인했다.
- commitment 보관 개수(256)와 보충 단위(64)는 고정값이다. 5절의 결과는 이 값에 따라 달라진다.
- handshake 측정의 서버는 quic-go 하나다.
- signer마다 CPU 하나만 썼고, host에서 실험과 무관한 프로세스가 CPU를 쓰고 있었다.

## 8. 설계에 주는 영향

- 모델의 loss 항을 일반화한다. signer m개를 쓰고 그중 t개의 응답으로 서명이 완성되는 구성에서, 서명이 재전송을 기다릴 확률은 m개 중 손실 없이 응답한 signer가 t개 미만일 확률이다. m = t이면 Phase 2의 식 1 − (1 − p)^(2Rt)가 된다.
- 최적화 문제의 결정 변수에 hedge의 폭 m을 넣고, 제약에 signer별 share 계산량과 commitment 소비량을 넣는다.
- 5절은 hedge의 폭을 고정하면 부하가 높을 때 손해라는 것을 보인다. 부하와 loss에 따라 폭을 바꾸는 정책이 다음 후보다. 예를 들어 commitment가 충분할 때만 폭을 넓히거나, 가까운 t개에 먼저 요청하고 일정 시간 안에 응답이 없을 때만 나머지에 요청하는 방식이다.
