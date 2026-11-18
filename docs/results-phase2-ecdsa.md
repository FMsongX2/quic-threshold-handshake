# Phase 2 결과: threshold ECDSA

측정일은 2026년 11월 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 측정 전의 예측은 [worklog.md](worklog.md)의 "남은 측정의 예측"에 있다. 원자료는 `results/2026-11-18-ecdsa`에 있다.

## 1. 배경과 방법

공개 WebPKI의 인증서에 쓸 수 있는 서명 알고리즘은 RSA와 ECDSA다. [results-phase2-signer.md](results-phase2-signer.md)는 그중 threshold RSA만 측정했다. 이 문서는 threshold ECDSA P-256을 같은 coordinator와 signer 구조에 넣어 측정한다.

구현은 coinbase/kryptology v1.8.0의 GG20(Gennaro와 Goldfeder의 2020년 protocol)이다. key는 dealer가 나눈다. signer끼리는 직접 통신하지 않고 coordinator가 protocol message를 모아 다시 나누어 준다. 그래서 protocol의 round 하나가 coordinator와 signer 사이의 요청 round 하나가 된다. 구현의 선택 이유는 [worklog.md](worklog.md)의 "threshold ECDSA 구현"에 있다.

- precomputation이 없으면 서명 하나에 요청 round가 6개다. 서명할 message는 마지막 round에만 들어간다.
- precomputation을 켜면 coordinator가 앞의 5개 round를 미리 끝낸 결과(presignature)를 signer 집합마다 256개 보관한다. 서명할 때는 round 하나만 쓴다. coordinator는 시작할 때 fastest가 고른 집합의 presignature를 채우고, 남은 수가 절반 이하가 되면 다시 채운다.
- presignature는 그것을 만든 signer 집합에 묶인다. 다른 집합으로 서명하려면 그 집합의 presignature가 따로 있어야 한다. 없으면 그 서명은 6개 round를 모두 거친다.

공통 조건은 다음과 같다. coordinator와 signer 사이는 signer당 유지되는 QUIC 연결 4개이고 정책은 fastest다. signer의 RTT_s는 모든 signer에 같은 값을 주었다. signer는 CPU 하나씩을 쓴다. handshake 측정의 RTT_c는 20 ms다. 조건 하나의 서명은 100~105개여서 측정 중에 presignature를 다시 채우는 일은 없다.

## 2. 요약

1. precomputation을 쓰면 D_sign 중앙값은 "RTT_s + signer 계산 시간"이었다. 계산 시간은 (2, 3)에서 14~18 ms, (3, 5)에서 35~36 ms다. RTT_s 5 ms에서 D_sign은 19.6 ms와 41.0 ms였다.
2. precomputation이 없으면 D_sign은 "6 × RTT_s + 계산 시간"이었고 계산 시간이 (2, 3)에서 222~275 ms, (3, 5)에서 497~697 ms였다. RTT_s 50 ms에서 D_sign은 574 ms와 1,010 ms다.
3. 실제 handshake에서 T_hs에서 D_sign을 뺀 값은 다섯 서버가 20.8~21.9 ms였다. 다른 scheme과 같다. RTT_s 5 ms, (2, 3)에서 T_hs는 44.2 ms로, precomputation을 쓰는 FROST(26.6 ms)와 threshold RSA(31~33 ms)보다 길다.
4. presignature 하나를 만드는 데 signer의 CPU 시간이 (2, 3)에서 약 0.23초, (3, 5)에서 약 0.45초 들었다. precomputation은 서명 지연을 줄이지만 서명 rate의 상한은 그대로 둔다. signer가 CPU 하나를 쓰면 지속할 수 있는 rate는 초당 약 4.3개와 2.2개다.
5. signer 집합이 바뀌면 precomputation의 효과가 사라졌다. precomputation을 쓴 조건 33개 중 10개에서 서명의 1~6%가 presignature 없이 6개 round를 거쳤다. 9개는 측정 중에 fastest가 고른 집합이 바뀐 조건이다. 새 집합의 presignature를 채우는 계산이 서명과 겹쳐 D_sign 중앙값이 21~898 ms가 됐고, (3, 5)에서는 handshake 100개 중 1~6개가 실패했다.
6. 동시 handshake 10개에서는 signer의 계산이 T_hs를 정했다. 서명을 차례로 기다리는 aioquic의 T_hs 중앙값(142.8 ms)이 나머지 서버(161.4~181.4 ms)보다 짧았다.

## 3. 서명 지연

coordinator만 실행해 서명을 하나씩 100회 요청했다. D_sign 열은 중앙값 / p95 / p99(ms)다. "네트워크"는 round별 왕복 시간의 합, "계산"은 round마다 가장 느린 signer의 계산 시간의 합이다(중앙값).

| precomputation | (t, n) | RTT_s | D_sign | round 수 | 네트워크 | 계산 |
| --- | --- | --- | --- | --- | --- | --- |
| 없음 | (2, 3) | 0 ms | 274.2 / 275.6 / 279.5 | 6 | 0.8 | 273.0 |
| 없음 | (2, 3) | 5 ms | 253.4 / 261.7 / 265.7 | 6 | 31.1 | 222.0 |
| 없음 | (2, 3) | 50 ms | 573.6 / 576.2 / 577.6 | 6 | 301.0 | 272.2 |
| 없음 | (3, 5) | 0 ms | 698.8 / 700.8 / 701.7 | 6 | 1.3 | 697.1 |
| 없음 | (3, 5) | 5 ms | 529.9 / 531.6 / 532.4 | 6 | 32.3 | 497.2 |
| 없음 | (3, 5) | 50 ms | 1,010.3 / 1,012.1 / 1,018.0 | 6 | 312.9 | 697.0 |
| 있음 | (2, 3) | 0 ms | 17.9 / 170.4 / 255.3 | 1 | 0.2 | 14.8 |
| 있음 | (2, 3) | 5 ms | 19.6 / 23.1 / 23.5 | 1 | 5.1 | 14.4 |
| 있음 | (2, 3) | 50 ms | 64.8 / 65.9 / 68.0 | 1 | 50.1 | 14.6 |
| 있음 | (3, 5) | 5 ms | 41.0 / 41.8 / 41.9 | 1 | 5.1 | 35.9 |
| 있음 | (3, 5) | 50 ms | 85.6 / 89.1 / 121.2 | 1 | 50.1 | 35.1 |

- 네트워크 항은 round 수에 RTT_s를 곱한 값이다. precomputation이 없으면 RTT_s 50 ms에서 301~313 ms, 있으면 50.1 ms다.
- 계산 항은 scheme 가운데 가장 크다. 같은 host에서 FROST는 0.2~0.3 ms, threshold RSA는 4.9 ms였다.
- 계산 시간은 실행마다 달랐다. (3, 5)의 497 ms와 697 ms는 고른 signer 집합의 차이다. signer 1과 signer 5는 같은 물리 core의 두 thread를 쓰는데, 둘이 함께 뽑힌 실행에서 계산이 1.4배 걸렸다. (2, 3)의 222 ms와 273 ms의 원인은 확인하지 않았다.
- precomputation을 쓴 (2, 3)의 RTT_s 0 ms 조건은 p95가 170.4 ms다. 측정 초반에 presignature를 채우는 계산이 남아 있었던 것으로 본다.
- precomputation을 쓴 (3, 5)의 RTT_s 0 ms 조건은 표에서 뺐다. 5절에서 다룬다.

### 3.1 signer link의 loss

(2, 3), RTT_s 5 ms, loss 2%다. 각 칸은 D_sign의 중앙값 / p95 / p99(ms)다.

| precomputation | signer 연결 | loss 없음 | loss 2% |
| --- | --- | --- | --- |
| 없음 | QUIC × 4 | 253.4 / 261.7 / 265.7 | 304.9 / 362.2 / 371.9 |
| 없음 | TCP × 1 | 측정하지 않음 | 316.0 / 714.4 / 771.3 |
| 있음 | QUIC × 4 | 19.6 / 23.1 / 23.5 | 23.0 / 82.9 / 126.7 |
| 있음 | TCP × 1 | 측정하지 않음 | 20.1 / 239.9 / 244.4 |

loss가 있는 QUIC 조건의 중앙값이 긴 것은 계산 시간의 차이다(273.3 ms와 17.9 ms). 네트워크 항의 중앙값은 31.1 ms와 5.1 ms로 loss가 없을 때와 같았다. p95와 중앙값의 차이는 QUIC에서 57~60 ms, TCP에서 220~398 ms다. precomputation이 없으면 round가 6개여서 TCP에서 재전송을 두 번 기다린 서명이 p95에 들어왔다.

## 4. handshake

다섯 서버에 같은 coordinator와 signer를 붙였다. 서버의 구성은 [results-phase2-implementations.md](results-phase2-implementations.md) 1절과 같다. s2n-quic은 P-256 key에서 s2n-tls를 쓴다. handshake를 하나씩 100회 했다. 각 칸은 T_hs 중앙값(ms)이고 괄호 안은 D_sign 중앙값이다. "*"는 측정 중에 signer 집합이 바뀐 조건이다.

| (t, n) | RTT_s | quic-go | quic-go + daemon | aioquic | quiche | s2n-quic |
| --- | --- | --- | --- | --- | --- | --- |
| (2, 3) | 5 ms | 100.7 (79.6) * | 44.2 (23.1) | 149.6 (129.3) * | 52.0 (31.3) * | 42.5 (20.7) * |
| (2, 3) | 50 ms | 86.3 (65.2) | 89.2 (68.1) | 86.5 (64.8) | 85.9 (65.1) | 85.7 (64.9) |
| (3, 5) | 5 ms | 67.4 (99.4) * | 832.2 (897.9) * | 619.1 (597.2) * | 61.5 (40.7) | 422.8 (403.0) * |
| (3, 5) | 50 ms | 384.4 (363.3) * | 121.8 (100.6) | 122.5 (100.7) | 121.5 (100.6) | 106.8 (86.0) |

signer 집합이 바뀌지 않은 조건 10개에서 T_hs 중앙값에서 D_sign 중앙값을 뺀 값은 다음과 같다.

| 서버 | T_hs − D_sign |
| --- | --- |
| quic-go | 21.03 ms |
| quic-go + daemon | 21.11~21.18 ms |
| aioquic | 21.68~21.86 ms |
| quiche | 20.77~20.92 ms |
| s2n-quic | 20.77~20.78 ms |

- 서명 지연이 T_hs에 그대로 더해지는 것은 ECDSA에서도 같다.
- (3, 5), RTT_s 50 ms에서 s2n-quic의 D_sign이 86.0 ms이고 다른 서버가 100.6 ms인 것은 signer 집합의 차이다. 다른 서버의 실행에서는 signer 1과 5가 함께 뽑혀 계산이 50.4 ms였고, s2n-quic의 실행에서는 35.9 ms였다.
- precomputation이 없는 (2, 3)을 quic-go 서버에 붙이면 RTT_s 50 ms에서 T_hs가 598.0 ms(D_sign 576.7 ms)였다. handshake 50개가 전부 성공했다.

같은 조건(RTT_c 20 ms, handshake를 하나씩)의 scheme별 T_hs 중앙값을 모으면 다음과 같다. ECDSA가 아닌 값은 [results-phase2-implementations.md](results-phase2-implementations.md) 3절의 quic-go 열이다.

| scheme | (t, n) | RTT_s 5 ms | RTT_s 50 ms |
| --- | --- | --- | --- |
| precomputation FROST | (3, 5) | 26.6 | 71.7 |
| FROST | (3, 5) | 31.8 | 121.9 |
| threshold RSA | (3, 5) | 31.4 | 78.2 |
| precomputation threshold ECDSA | (2, 3) | 44.2 | 86.3 |
| precomputation threshold ECDSA | (3, 5) | 61.5 | 106.8~122.5 |
| threshold ECDSA | (2, 3) | 303.0 | 598.0 |

### 4.1 동시 handshake 10개

(2, 3), precomputation, RTT_s 5 ms다. handshake 10개를 함께 시작하는 것을 10회 반복했다.

| 서버 | T_hs 중앙값 | T_hs p99 | D_sign 중앙값 |
| --- | --- | --- | --- |
| quic-go | 161.4 ms | 175.3 ms | 140.4 ms |
| quic-go + daemon | 181.4 ms | 206.3 ms | 159.6 ms |
| aioquic | 142.8 ms | 263.3 ms | 23.2 ms |
| quiche | 173.8 ms | 206.8 ms | 152.9 ms |
| s2n-quic | 167.5 ms | 205.5 ms | 146.5 ms |

서명 요청 10개가 함께 도착하면 signer는 그것들을 동시에 계산하고, CPU가 하나이므로 모두가 비슷한 때에 끝난다. D_sign 중앙값이 요청 하나의 계산 시간의 약 10배(140~160 ms)가 됐다. aioquic은 서명을 하나씩 요청하므로 signer가 한 번에 하나만 계산한다. 서명 하나는 23.2 ms에 끝나고 handshake는 차례로 완료되어 중앙값이 142.8 ms였다. 마지막 handshake가 끝나는 시점은 비슷하므로 p99는 aioquic이 더 길다.

## 5. signer 집합이 바뀔 때

RTT_s가 모두 같으면 fastest가 고르는 집합은 RTT 추정값의 작은 차이로 정해진다. precomputation을 쓴 조건 33개 중 9개에서 측정 중에 집합이 바뀌었다(서명에 쓰인 signer가 t개보다 많았다). 다른 한 조건은 coordinator가 시작할 때 채운 집합과 측정 중에 쓴 집합이 달랐다. 이 10개 조건에서만 presignature 없이 진행한 서명이 나왔다.

| 조건 | 서버 | presignature 없이 진행한 서명 | D_sign 중앙값 | 성공한 handshake |
| --- | --- | --- | --- | --- |
| (2, 3), RTT_s 5 ms | quic-go | 1% | 79.6 ms | 100 / 100 |
| (2, 3), RTT_s 5 ms | aioquic | 2% | 129.3 ms | 100 / 100 |
| (2, 3), RTT_s 5 ms | quiche | 1% | 31.3 ms | 100 / 100 |
| (2, 3), RTT_s 5 ms | s2n-quic | 1% | 20.7 ms | 100 / 100 |
| (3, 5), RTT_s 5 ms | quic-go | 3.8% | 99.4 ms | 99 / 100 |
| (3, 5), RTT_s 5 ms | quic-go + daemon | 5.9% | 897.9 ms | 94 / 100 |
| (3, 5), RTT_s 5 ms | aioquic | 5.1% | 597.2 ms | 97 / 100 |
| (3, 5), RTT_s 5 ms | s2n-quic | 5.9% | 403.0 ms | 97 / 100 |
| (3, 5), RTT_s 50 ms | quic-go | 5.9% | 363.3 ms | 97 / 100 |
| (3, 5), RTT_s 0 ms | 서버 없음(3절의 방법) | 3% | 634.3 ms | 서명 100개 중 1개 실패 |

- 새 집합을 처음 고른 서명은 presignature가 없어 6개 round를 거친다. 동시에 coordinator가 그 집합의 presignature 256개를 채우기 시작한다. 채우는 계산이 signer의 CPU를 쓰는 동안에는 presignature가 있는 서명도 늦어진다.
- 채우는 데 걸린 시간은 coordinator를 시작한 때부터 첫 서명까지의 시간으로 잴 수 있다. (2, 3)에서 약 60초, (3, 5)에서 약 115초였다. presignature 하나에 0.23초와 0.45초다. RTT_s 5 ms와 50 ms에서 같았으므로 계산이 정하는 시간이다.
- (3, 5)는 집합이 10가지이고 채우는 시간이 길어, 집합이 바뀐 조건에서 서명이 제한 시간 15초를 넘겨 handshake가 실패했다.
- 집합이 바뀌어도 서명의 1%만 6개 round를 거친 (2, 3) 조건에서는 중앙값이 20.7~31.3 ms로 거의 그대로인 경우도 있었다. 채우는 동안 다시 원래 집합으로 돌아갔기 때문으로 본다.

RTT_s가 signer마다 다른 배치에서는 fastest의 집합이 흔들리지 않는다. 그 대신 signer 하나가 느려지거나 응답하지 않아 집합을 바꿔야 할 때 같은 일이 생긴다. hedge와 first는 요청마다 여러 집합을 쓰므로 이 구현에서는 지원하지 않는다.

## 6. 예측과 판정

| 예측 | 결과 | 판정 |
| --- | --- | --- |
| precomputation이 없으면 D_sign은 "6 × RTT_s + 계산 시간"이다. RTT_s 50 ms와 5 ms의 차이는 270 ms다. | 네트워크 항의 차이는 270 ms와 281 ms였다. D_sign의 차이는 계산 시간이 실행마다 달라 320 ms와 480 ms였다. | 네트워크 항은 맞음 |
| precomputation이 있으면 "RTT_s + 계산 시간"이고 계산 시간은 (2, 3)에서 15~20 ms, (3, 5)에서 30~40 ms다. | 14.4~17.9 ms, 35.1~35.9 ms. signer 1과 5가 함께 뽑히면 50.4 ms. RTT_s 50 ms와 5 ms의 차이는 45.2 ms와 44.6 ms. | 맞음 |
| handshake를 하나씩 할 때 T_hs − D_sign은 다섯 서버 모두 20.6~21.8 ms다. | 20.77~21.86 ms | 맞음 |
| 동시 handshake 10개에서 한 wave가 130 ms 이상 걸리고, aioquic과 나머지 서버의 T_hs 중앙값 차이는 1.5배 안이다. | 나머지 서버 161.4~181.4 ms, aioquic 142.8 ms. aioquic이 더 짧았다. | 맞음 |
| signer link의 loss 2%에서 p95가 QUIC에서 37 ms 이상, TCP에서 210 ms 이상 늘어난다. | p95와 중앙값의 차이가 QUIC 57~60 ms, TCP 220~398 ms | 맞음 |

signer 집합이 바뀔 때 precomputation의 효과가 사라지는 것과 그 때문에 handshake가 실패하는 것은 예측에 없었다.

## 7. 한계

- library는 갱신이 멈춘 상태이고 GG20은 저자들이 쓰지 말라고 밝힌 protocol이다. 이 측정은 지연의 크기를 보려는 것이며 실제 배치의 근거가 아니다. 더 새로운 protocol(CGGMP21, DKLs23)은 round 수와 계산 시간이 다르다.
- library가 마지막 round의 검증을 따로 내보내지 않아, precomputation을 켜도 signer가 서명마다 다른 signer의 proof를 검증한다. 계산 시간 14~36 ms의 상당 부분이 이 검증일 수 있다. 분해하지 않았다.
- key는 dealer가 나누었다. 분산 key 생성은 측정하지 않았다.
- 조건마다 서명이 100개 안팎이어서 p99는 표본 하나로 정해진다.
- RTT_s를 모든 signer에 같게 준 탓에 signer 집합이 바뀐 조건이 10개 나왔다. 그 조건들은 5절에서만 쓰고 지연의 크기를 말할 때는 뺐다. 집합을 고정하는 정책(fixed)으로 다시 측정하지 않았다.
- presignature를 채우는 시간은 조건의 시작부터 첫 서명까지의 시간으로 어림한 값이다. coordinator가 직접 기록한 값이 아니다.
- signer의 계산 시간이 실행마다 최대 1.4배 달랐다. signer 5개 가운데 둘이 같은 물리 core를 쓰는 testbed의 제약이다.
- 정책은 fastest 하나, signer 연결은 QUIC 4개 하나(loss 조건만 TCP 포함)다. 동시 handshake는 10개, (2, 3)만 측정했다.
- 측정 중 host의 1분 load average는 3.0~6.6이었다.

## 8. 설계에 주는 영향

- 공개 WebPKI에 쓸 수 있는 두 scheme을 같은 조건에서 비교하면, handshake를 하나씩 할 때 threshold RSA (3, 5)가 precomputation을 쓰는 threshold ECDSA (3, 5)보다 RTT_s 5 ms에서 30 ms, 50 ms에서 29~44 ms 짧았다. threshold RSA는 round가 하나이고 미리 계산할 것이 없다.
- threshold ECDSA의 서명 rate는 presignature를 만드는 계산이 정한다. 이 구현에서 signer의 CPU 하나당 초당 2~4개다. threshold RSA signer는 같은 host에서 초당 약 200개였다([results-phase2-environment.md](results-phase2-environment.md) 6절).
- presignature가 signer 집합에 묶이는 scheme에서는 집합을 바꾸는 일이 곧 지연과 실패다. signer 선택 정책은 집합을 오래 유지해야 하고, 장애에 대비하려면 대체 집합의 presignature를 미리 채워 두어야 한다. 그만큼 계산 비용이 는다.
- 최적화 문제에서 threshold ECDSA는 지연의 중앙값뿐 아니라 서명 rate 제약과 집합 전환 비용을 함께 넣어야 비교할 수 있다. 이 연구의 모델에는 그 두 항이 없으므로 Phase 3의 구성 비교에서는 threshold ECDSA를 뺀다.
