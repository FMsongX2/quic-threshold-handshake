# Phase 3 결과: signer 연결의 TLS와 QUIC

측정일은 2026년 11월 17일과 18일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 구현 결정과 측정 전의 예측은 [worklog.md](worklog.md)에 있다. 원자료는 `results/2026-11-17-signer-*`에 있다.

## 1. 배경과 방법

[results-phase3-connections.md](results-phase3-connections.md)는 coordinator와 signer 사이 transport의 기본 구성을 "signer당 TCP 연결 여러 개 + ping nudge"로 정하고, QUIC 연결 하나로 같은 결과를 얻는지를 다음 확인 대상으로 남겼다. 그때까지의 signer 연결은 암호화가 없는 TCP였다. 이 문서는 signer 연결에 TLS 1.3과 QUIC을 쓰는 transport를 구현해 세 가지를 측정한다. 연결을 유지할 때 손실 뒤의 지연, handshake가 몰릴 때의 지연, 요청마다 연결을 새로 맺을 때의 비용이다.

| transport | 연결 | 요청 하나의 전달 |
| --- | --- | --- |
| `persistent` | TCP, 유지 | 연결 하나에 요청 ID로 다중화 |
| `tls` | TCP + TLS 1.3, 유지 | 위와 같음 |
| `quic` | QUIC, 유지 | 요청마다 stream 하나 |
| `per-request` | TCP, 요청마다 새로 | 연결을 맺고 요청 하나를 보낸 뒤 닫음 |
| `tls-per-request` | TCP + TLS 1.3, 요청마다 새로 | 매번 전체 handshake |
| `quic-per-request` | QUIC, 요청마다 새로 | 매번 전체 handshake |
| `quic-0rtt` | QUIC, 요청마다 새로 | 앞선 연결에서 받은 session ticket으로 요청을 0-RTT data로 보냄 |

QUIC 구현체는 quic-go v0.65.0이고 TLS는 Go 표준 library다. signer는 시작할 때 자체 서명한 Ed25519 인증서를 만들고, coordinator는 인증서의 chain과 이름을 검증하지 않는다. handshake의 서명 검증은 수행된다. 아래에서 "TCP × 64"는 signer당 TCP 연결 64개, "QUIC × 4"는 signer당 QUIC 연결 4개를 뜻한다.

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 유지되는 연결, 요청을 하나씩 | `2026-11-17-signer-quic-policy` | precomputation FROST (2, 5)와 (3, 5), TCP·TLS·QUIC, nudge 유무, loss {0, 2}%, 조건마다 서명 1,000회 |
| 유지되는 연결, 동시 handshake | `2026-11-17-signer-quic-conc` | 같은 scheme, TCP × {1, 64}, QUIC × {1, 4, 16}, 0-RTT QUIC, nudge 유무, loss {0, 2}%, 동시 handshake 100개를 50회 |
| QUIC 연결의 qlog | `2026-11-17-signer-quic-qlog` | precomputation FROST (3, 5), QUIC × {1, 16}, loss {0, 2}%, 동시 handshake 100개를 50회. coordinator 쪽 qlog를 기록 |
| 요청마다 새 연결 | `2026-11-17-signer-setup` | precomputation FROST (3, 5), FROST (3, 5), threshold RSA (2, 5), transport 5종, loss {0, 2}%, 조건마다 서명 1,000회 |

signer 선택 정책은 모두 fastest(가장 가까운 t개 signer)다. signer 5개의 RTT_s는 signer 번호 순서대로 50, 5, 100, 1, 20 ms이고 지연은 signer의 송신 방향에 걸었다. 따라서 (2, 5)는 RTT_s가 1, 5 ms인 signer를, (3, 5)는 1, 5, 20 ms인 signer를 쓴다. loss는 coordinator와 signer의 송신 방향에 각각 주었다. handshake 측정에서 RTT_c는 20 ms이고 client와 서버 사이에는 loss를 주지 않았다. 측정 중 host의 1분 load average는 1.7~4.5였다. qlog 원본은 저장소에 넣지 않았다.

## 2. 요약

1. 손실된 packet 뒤에 다른 packet이 없을 때, 그 요청의 응답 시간은 signer의 RTT_s를 r이라 하면 TCP에서 2r + 206~213 ms, QUIC에서 2r + 27 ms 남짓이었다. 요청을 하나씩 보내고 loss가 2%일 때 (3, 5)의 D_sign p95는 TCP 237.2 ms, QUIC 42.7 ms다. QUIC은 nudge 없이 TCP + nudge(43.7 ms)와 같은 수준이다.
2. handshake 100개가 동시에 시작되고 loss가 2%일 때, QUIC 연결 하나로는 T_hs 중앙값이 loss가 없을 때의 값으로 돌아오지 않았다. (3, 5)에서 61.8 ms로, loss가 없을 때의 50.9 ms보다 11.0 ms 길다. 측정 전의 예측이 틀렸다.
3. 원인은 congestion window였다. qlog에서 RTT_s가 20 ms인 signer로 가는 연결의 congestion window는 loss가 없을 때 63.5 packet, loss 2%에서 11.9 packet이었고, 한 번에 몰린 요청의 절반은 처음 전송되기까지 14.1 ms 이상을 기다렸다. stream마다 전달 순서가 독립이어도 연결 하나의 전송량 제한은 남는다.
4. QUIC 연결을 signer당 4개로 늘리면 중앙값이 돌아왔다. (3, 5)에서 T_hs는 49.6 / 64.9 / 77.0 ms(중앙값 / p95 / p99)로, nudge 없이 TCP × 64 + nudge의 49.2 / 69.5 / 101.3 ms보다 꼬리가 짧다.
5. 이 결과는 [results-phase3-connections.md](results-phase3-connections.md)의 해석을 고친다. 한 연결에 몰린 요청이 느려지는 이유를 "손실 하나를 함께 기다린다"로만 설명했는데, 줄어든 congestion window도 원인이다. TCP에서 두 원인의 몫은 나누지 못했다.
6. 요청마다 연결을 새로 맺으면 loss가 없을 때 요청 하나의 왕복 수는 TCP 2, TLS를 쓰는 TCP 3, QUIC 2, 0-RTT QUIC 1이었고 예측과 일치했다. 0-RTT QUIC의 D_sign 중앙값은 유지되는 연결보다 0.5~0.8 ms 길었다.
7. loss가 2%이면 요청마다 맺는 TCP 연결의 D_sign p95는 TLS 유무와 관계없이 1.03~1.21초였다. 같은 조건에서 QUIC은 57~282 ms, 0-RTT QUIC은 29~241 ms였다.
8. 0-RTT QUIC은 handshake가 몰리면 느렸다. loss가 없을 때 T_hs 중앙값은 (2, 5)에서 67.3~69.8 ms, (3, 5)에서 86.3~90.5 ms로, 유지되는 연결의 35~37 ms와 48~51 ms보다 30~42 ms 길다.

## 3. 측정 전의 예측과 판정

예측은 구현을 올린 commit과 함께 [worklog.md](worklog.md)에 기록했다.

| 번호 | 예측 | 결과 | 판정 |
| --- | --- | --- | --- |
| 1 | QUIC 연결 하나이면 동시 handshake 100개, loss 2%에서 T_hs 중앙값이 loss가 없을 때와 같다. | (2, 5)에서 1.3~2.7 ms, (3, 5)에서 11.0~14.4 ms 길었다. 연결 4개에서는 같았다. | 틀림 |
| 2 | nudge가 없을 때 손실이 난 요청의 응답 시간은 약 2r + 26 ms다. 1, 5, 20 ms에서 28, 36, 66 ms다. | 29.4~29.5, 37.6, 67.8 ms | 맞음 |
| 3 | 요청을 하나씩 보내고 loss 2%일 때 D_sign p95는 (2, 5)와 (3, 5)에서 30~40 ms, (3, 5)의 p99는 약 66 ms다. | p95 30.0 ms와 42.7 ms, p99 68.4 ms | (3, 5)의 p95가 범위를 2.7 ms 벗어남 |
| 4 | QUIC + nudge의 p95는 (2, 5)에서 20 ms 이하이고, (3, 5)에서는 nudge가 있어도 달라지지 않는다. | (2, 5) 12.8 ms. (3, 5)는 p95 42.7 → 39.0 ms, p99 68.4 → 73.5 ms | 대체로 맞음 |
| 5 | loss가 없고 handshake가 몰릴 때 QUIC이 TCP보다 느릴 수 있다. | QUIC의 T_hs 중앙값이 TCP보다 최대 2.3 ms 길었다. 반복 측정의 차이와 구분되지 않는다. | 확인되지 않음 |
| 6 | 요청마다 연결을 맺을 때 요청 하나의 왕복 수는 TCP 2, TLS 3, QUIC 2, 0-RTT QUIC 1이다. | D_sign 중앙값이 모델과 0.1~2.1 ms 차이 | 맞음 |
| 7 | loss 2%에서 연결 수립 packet의 손실은 TCP에서 약 1초, quic-go에서 약 200 ms의 대기를 만든다. | TCP에서 서명의 6.8~22.2%가 1초 넘게 늦었다. QUIC에서는 0.1% 이하였고 p95가 57~282 ms였다. | 맞음 |

## 4. 유지되는 연결, 요청을 하나씩 보낼 때

서명을 하나씩 순서대로 요청했고 조건마다 1,000회다. 24개 조건 모두에서 1,000회가 전부 성공했다. D_sign 열은 중앙값 / p95 / p99(ms)다.

| (t, n) | transport | nudge | D_sign, loss 0% | D_sign, loss 2% |
| --- | --- | --- | --- | --- |
| (2, 5) | TCP | 없음 | 5.31 / 5.65 / 8.98 | 5.32 / 214.42 / 227.30 |
| (2, 5) | TCP + TLS | 없음 | 5.31 / 5.73 / 8.87 | 5.32 / 214.56 / 230.73 |
| (2, 5) | QUIC | 없음 | 5.37 / 5.72 / 8.95 | 5.38 / 29.96 / 38.05 |
| (2, 5) | TCP | 1.5 | 5.32 / 5.70 / 8.84 | 5.33 / 13.65 / 20.68 |
| (2, 5) | TCP + TLS | 1.5 | 5.30 / 5.92 / 7.04 | 5.33 / 12.72 / 20.69 |
| (2, 5) | QUIC | 1.5 | 5.37 / 6.01 / 8.96 | 5.39 / 12.77 / 20.99 |
| (3, 5) | TCP | 없음 | 20.35 / 20.58 / 22.04 | 20.40 / 237.19 / 258.13 |
| (3, 5) | TCP + TLS | 없음 | 20.37 / 20.60 / 23.76 | 20.40 / 241.73 / 256.14 |
| (3, 5) | QUIC | 없음 | 20.43 / 20.75 / 23.28 | 20.44 / 42.67 / 68.41 |
| (3, 5) | TCP | 1.5 | 20.36 / 20.56 / 21.64 | 20.38 / 43.73 / 73.08 |
| (3, 5) | TCP + TLS | 1.5 | 20.37 / 20.64 / 22.10 | 20.39 / 42.87 / 73.29 |
| (3, 5) | QUIC | 1.5 | 20.42 / 20.60 / 22.20 | 20.43 / 39.01 / 73.51 |

loss가 없으면 세 transport의 D_sign 중앙값은 0.1 ms 안에서 같다. 연결을 유지할 때 TLS는 TCP와 차이가 없었다.

loss 2%, nudge 없는 조건에서 응답이 늦은 요청만 모아 signer별 응답 시간의 중앙값을 구하면 다음과 같다. 응답 시간은 요청을 보낸 뒤 응답을 받을 때까지의 시간에서 signer의 계산 시간을 뺀 값이고, "늦은 요청"은 이 값이 그 signer의 중앙값의 1.5배에 3 ms를 더한 값을 넘는 요청이다. 범위는 (2, 5)와 (3, 5) 조건의 값이다.

| signer의 RTT_s (r) | TCP | TCP + TLS | QUIC | 2r + 27 ms |
| --- | --- | --- | --- | --- |
| 1 ms | 209.4~214.7 ms | 210.1~211.0 ms | 29.4~29.5 ms | 29 ms |
| 5 ms | 219.0~220.6 ms | 218.2~219.0 ms | 37.6 ms | 37 ms |
| 20 ms | 248.4 ms | 246.4 ms | 67.8 ms | 67 ms |

QUIC의 값은 probe timeout으로 설명된다. QUIC은 ack-eliciting packet을 보낸 뒤 "smoothed RTT + max(RTT 편차의 4배, timer 단위) + 상대의 max_ack_delay" 동안 응답이 없으면 probe를 보낸다[3]. quic-go의 timer 단위는 1 ms이고 상대에게 알리는 max_ack_delay는 26 ms다(qlog에 기록된 transport parameter에서 확인했다). RTT 편차가 작으면 probe timeout은 r + 27 ms이고, probe로 다시 간 요청의 응답이 r 뒤에 오므로 합이 2r + 27 ms다.

TCP의 값은 2r에 206~213 ms를 더한 값이다. Linux TCP는 재전송 timer에 200 ms의 하한(`TCP_RTO_MIN`)을 둔다[29]. 측정값은 이 하한과 맞지만 TCP의 동작을 packet 단위로 확인하지는 않았다. 같은 날의 다른 측정(6.2절의 유지되는 TCP 연결)에서는 같은 조건의 값이 2r + 225 ms까지 나왔다.

두 transport에서 nudge의 효과는 다르다.

- TCP에서는 nudge가 꼬리를 정한다. p95가 (2, 5)에서 214.4 ms에서 13.7 ms로, (3, 5)에서 237.2 ms에서 43.7 ms로 줄었다.
- QUIC에서는 nudge의 지연 시간이 probe timeout보다 짧을 때만 효과가 있다. (2, 5)의 nudge 지연 시간은 9.5 ms여서 p95가 30.0 ms에서 12.8 ms로 줄었다. (3, 5)의 nudge 지연 시간은 32 ms로, 가까운 두 signer의 probe timeout(28, 32 ms)보다 빠르지 않다.
- (3, 5)에서 RTT_s가 20 ms인 signer의 늦은 응답은 nudge가 있을 때 72.9 ms로, 없을 때의 67.8 ms보다 5.1 ms 길었다. QUIC은 ack-eliciting packet을 보낼 때마다 probe timeout을 다시 잰다[3]. nudge의 ping이 그 packet이어서 probe가 미뤄진 것으로 해석한다.

## 5. 유지되는 연결, handshake가 몰릴 때

quic-go 서버의 인증서 key를 coordinator로 두고, client가 handshake 100개를 같은 시각에 시작하는 것을 50회 반복해 조건마다 5,000개를 측정했다. 이 절의 40개 조건 모두에서 5,000개가 전부 성공했다. T_hs 열은 중앙값 / p95 / p99(ms)다. "늦은 handshake"는 T_hs가 그 조건의 중앙값보다 100 ms 넘게 긴 handshake의 비율이다.

### 5.1 signer 링크 loss 2%

| (t, n) | signer 연결 | nudge | T_hs | 늦은 handshake |
| --- | --- | --- | --- | --- |
| (2, 5) | TCP × 1 | 없음 | 42.82 / 62.31 / 148.24 | 2.60% |
| (2, 5) | TCP × 1 | 1.5 | 42.49 / 53.59 / 58.58 | 0.00% |
| (2, 5) | TCP × 64 | 없음 | 34.13 / 237.91 / 258.66 | 5.44% |
| (2, 5) | TCP × 64 | 1.5 | 35.58 / 47.13 / 55.94 | 0.42% |
| (2, 5) | QUIC × 1 | 없음 | 37.72 / 50.51 / 82.68 | 0.00% |
| (2, 5) | QUIC × 1 | 1.5 | 37.37 / 48.42 / 71.08 | 0.00% |
| (2, 5) | QUIC × 4 | 없음 | 35.82 / 44.74 / 47.62 | 0.00% |
| (2, 5) | QUIC × 4 | 1.5 | 36.38 / 45.55 / 49.54 | 0.00% |
| (2, 5) | QUIC × 16 | 없음 | 36.35 / 46.12 / 51.18 | 0.00% |
| (2, 5) | QUIC × 16 | 1.5 | 36.25 / 46.13 / 49.95 | 0.00% |
| (3, 5) | TCP × 1 | 없음 | 88.59 / 145.02 / 211.57 | 1.60% |
| (3, 5) | TCP × 1 | 1.5 | 97.15 / 143.82 / 217.75 | 1.18% |
| (3, 5) | TCP × 64 | 없음 | 48.51 / 246.71 / 286.26 | 6.84% |
| (3, 5) | TCP × 64 | 1.5 | 49.24 / 69.51 / 101.31 | 0.36% |
| (3, 5) | QUIC × 1 | 없음 | 61.83 / 101.60 / 109.79 | 0.00% |
| (3, 5) | QUIC × 1 | 1.5 | 63.59 / 103.66 / 121.00 | 0.00% |
| (3, 5) | QUIC × 4 | 없음 | 49.56 / 64.85 / 77.03 | 0.00% |
| (3, 5) | QUIC × 4 | 1.5 | 49.65 / 63.76 / 73.66 | 0.00% |
| (3, 5) | QUIC × 16 | 없음 | 49.23 / 65.10 / 82.78 | 0.02% |
| (3, 5) | QUIC × 16 | 1.5 | 48.94 / 61.83 / 76.75 | 0.00% |

QUIC 연결 하나는 TCP 연결 하나보다 빠르지만 loss가 없을 때의 값에는 미치지 못했다. (3, 5)의 중앙값은 TCP × 1이 88.6 ms, QUIC × 1이 61.8 ms이고, loss가 없을 때의 QUIC × 1은 50.9 ms다. 100 ms 넘게 늦은 handshake는 QUIC의 모든 조건에서 0.02% 이하였다.

QUIC 연결을 4개로 늘리면 중앙값이 loss가 없을 때의 값(5.2절의 35.6 ms와 49.0 ms)과 같아졌다. 16개로 늘려도 더 줄지 않았다. QUIC × 4는 nudge 없이 TCP × 64 + nudge보다 꼬리가 짧다. p99가 (2, 5)에서 47.6 ms 대 55.9 ms, (3, 5)에서 77.0 ms 대 101.3 ms다. 연결 여러 개인 QUIC에서 nudge는 결과를 바꾸지 않았다.

### 5.2 signer 링크 loss 0%

| (t, n) | signer 연결 | T_hs, nudge 없음 | T_hs, nudge 1.5 |
| --- | --- | --- | --- |
| (2, 5) | TCP × 1 | 35.41 / 43.10 / 46.23 | 34.93 / 41.27 / 44.46 |
| (2, 5) | TCP × 64 | 34.97 / 42.53 / 47.47 | 35.12 / 42.95 / 46.29 |
| (2, 5) | QUIC × 1 | 35.00 / 42.11 / 45.07 | 36.06 / 43.94 / 47.41 |
| (2, 5) | QUIC × 4 | 35.57 / 43.86 / 46.99 | 37.23 / 51.15 / 61.90 |
| (2, 5) | QUIC × 16 | 36.64 / 45.58 / 49.61 | 36.41 / 45.32 / 49.76 |
| (3, 5) | TCP × 1 | 49.01 / 58.25 / 67.59 | 49.85 / 59.66 / 66.10 |
| (3, 5) | TCP × 64 | 48.77 / 56.78 / 59.56 | 49.13 / 58.19 / 62.81 |
| (3, 5) | QUIC × 1 | 50.86 / 64.82 / 79.04 | 49.20 / 59.78 / 65.02 |
| (3, 5) | QUIC × 4 | 48.97 / 58.13 / 74.03 | 48.85 / 56.79 / 59.87 |
| (3, 5) | QUIC × 16 | 49.01 / 57.79 / 61.64 | 48.34 / 56.87 / 59.07 |

loss가 없으면 T_hs 중앙값은 (2, 5)에서 34.9~37.2 ms, (3, 5)에서 48.3~50.9 ms 범위다. nudge 조건이 같은 것끼리 비교하면 QUIC의 중앙값은 TCP보다 최대 2.3 ms 길었다. nudge 유무만 다른 두 조건 사이의 차이가 최대 1.7 ms이므로 이 차이를 QUIC의 비용으로 단정할 수 없다.

### 5.3 QUIC 연결 하나에서 중앙값이 늘어난 원인

QUIC은 stream마다 전달 순서가 독립이므로, 손실 하나가 다른 요청의 전달을 막는 일은 없다. 그런데도 QUIC × 1의 중앙값이 늘어난 이유를 보려고 coordinator가 signer와 맺은 QUIC 연결의 qlog를 기록해 다시 측정했다. qlog를 켠 측정의 T_hs 중앙값은 QUIC × 1에서 50.2 ms(loss 0%)와 62.8 ms(loss 2%)로, 켜지 않은 측정의 50.9 ms, 61.8 ms와 같은 수준이다.

아래 표는 동시에 시작된 handshake 100개(wave) 하나마다 값을 구한 뒤, 50개 wave의 중앙값을 적은 것이다. "전송 대기"는 서명 요청이 처음 packet에 실려 나간 시각과 coordinator가 그 서명을 시작한 시각의 차이로, wave 안의 순서가 같은 것끼리 비교했다. "전송 뒤 응답 시간"은 요청이 처음 나간 뒤 응답이 도착할 때까지의 시간이다. congestion window는 wave가 시작할 때의 값을 1,280 byte packet 수로 나타냈다.

| signer의 RTT_s | signer 연결 | loss | congestion window | 요청이 실린 packet 수 | 전송 대기, 50번째 요청 | 전송 대기, 90번째 요청 | 전송 뒤 응답 시간, 중앙값 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 20 ms | QUIC × 1 | 0% | 63.5 | 81 | 0.0 ms | 0.1 ms | 23.2 ms |
| 20 ms | QUIC × 1 | 2% | 11.9 | 52 | 14.1 ms | 25.9 ms | 21.1 ms |
| 20 ms | QUIC × 16 | 2% | 연결마다 다름 | 100 | 0.0 ms | 0.0 ms | 22.0 ms |
| 5 ms | QUIC × 1 | 0% | 54.5 | - | 2.9 ms | 0.6 ms | 9.5 ms |
| 5 ms | QUIC × 1 | 2% | 12.3 | - | 3.5 ms | 5.4 ms | 9.1 ms |

loss가 2%이면 congestion window가 63.5 packet에서 11.9 packet으로 줄었다(wave별 값의 10번째, 90번째 백분위는 8.8, 17.3 packet). quic-go의 congestion control은 손실을 혼잡으로 보고 window를 줄인다. wave 하나의 요청은 packet 수십 개에 실리므로 window를 넘는 요청은 앞선 packet의 ACK가 올 때까지 전송되지 못한다. RTT_s가 20 ms인 signer에서 wave의 50번째 요청은 14.1 ms, 90번째 요청은 25.9 ms를 기다렸다. 전송된 뒤의 응답 시간은 loss가 없을 때와 같았다(23.2 ms 대 21.1 ms).

ACK는 RTT_s마다 돌아오므로 이 대기는 RTT_s가 큰 signer에서 크다. RTT_s가 5 ms인 signer의 연결도 window는 12.3 packet으로 줄었지만 전송 대기는 wave의 90번째 요청에서 5.4 ms였다. (2, 5)가 (3, 5)보다 덜 늘어난 것(1.3~2.7 ms 대 11.0~14.4 ms)과 맞는다.

연결이 16개이면 요청이 연결마다 6~7개씩 나뉘고, 전송 대기가 사라졌다.

### 5.4 이전 해석의 수정

[results-phase3-connections.md](results-phase3-connections.md) 5절은 TCP 연결 하나에 몰린 요청이 느려지는 경로를 "손실 하나가 같은 연결의 뒤따르는 요청과 응답을 함께 늦춘다"로 적었다. 이 절의 결과는 그 설명이 충분하지 않음을 보인다. 전달 순서가 독립인 QUIC에서도 연결 하나에 몰린 요청은 느려졌고, 그 원인은 줄어든 congestion window였다. TCP도 손실에 congestion window를 줄이므로 TCP × 1의 지연에는 두 원인이 함께 들어 있을 것으로 본다. TCP 쪽의 window는 측정하지 않았고 두 원인의 몫을 나누지 못했다. TCP × 64가 중앙값을 되돌린 것은 두 설명 모두와 맞는다. 연결마다 요청이 한두 개여서 함께 기다릴 요청도 없고 window를 넘지도 않는다.

"연결을 늘리면 중앙값이 돌아오고, 연결을 늘릴 때 TCP에서는 nudge가 필요하다"는 측정 결과는 그대로다.

## 6. 요청마다 연결을 새로 맺을 때

서명을 하나씩 순서대로 요청했고 조건마다 1,000회다. FROST (3, 5)는 precomputation이 없어 서명 하나에 round가 2개다. threshold RSA (2, 5)는 RTT_s가 1, 5 ms인 signer를 쓴다. 0-RTT QUIC의 모든 조건에서 측정 구간의 요청은 전부 0-RTT data로 전달됐다.

### 6.1 signer 링크 loss 0%

D_sign의 중앙값(ms)이다. 괄호 안은 유지되는 TCP 연결과의 차이다.

| transport | 요청 하나의 왕복 수 | precomputation FROST (3, 5) | FROST (3, 5) | threshold RSA (2, 5) |
| --- | --- | --- | --- | --- |
| TCP, 유지 | 1 | 20.37 | 40.53 | 9.13 |
| TCP, 요청마다 | 2 | 40.43 (+20.06) | 80.67 (+40.14) | 14.34 (+5.21) |
| TCP + TLS, 요청마다 | 3 | 60.91 (+40.54) | 121.61 (+81.08) | 20.76 (+11.63) |
| QUIC, 요청마다 | 2 | 41.05 (+20.68) | 81.89 (+41.36) | 16.27 (+7.14) |
| 0-RTT QUIC, 요청마다 | 1 | 20.82 (+0.45) | 41.34 (+0.81) | 9.97 (+0.84) |

늘어난 시간은 "(왕복 수 − 1) × round 수 × 선택된 signer 중 최대 RTT_s"와 맞는다. 최대 RTT_s는 FROST에서 20 ms, threshold RSA에서 5 ms다. 전체 handshake를 하는 TLS와 QUIC은 이 값보다 0.5~2.1 ms 더 걸렸다. 0-RTT QUIC은 연결을 유지하지 않고도 유지되는 연결과 0.5~0.8 ms 차이였다.

### 6.2 signer 링크 loss 2%

D_sign 열은 중앙값 / p95 / p99 / 최댓값(ms)이다. "1초 이상 늦음"은 D_sign이 그 조건의 중앙값보다 900 ms 넘게 긴 서명의 비율이다.

| scheme | transport | D_sign | 1초 이상 늦음 | 실패 |
| --- | --- | --- | --- | --- |
| precomputation FROST (3, 5) | TCP, 유지 | 20.38 / 244.68 / 258.28 / 479.0 | 0.0% | 0 |
| precomputation FROST (3, 5) | TCP, 요청마다 | 40.50 / 1052.78 / 1098.19 / 3433.2 | 11.7% | 0 |
| precomputation FROST (3, 5) | TCP + TLS, 요청마다 | 60.99 / 1080.51 / 1292.02 / 4045.9 | 11.5% | 1 |
| precomputation FROST (3, 5) | QUIC, 요청마다 | 41.15 / 203.55 / 241.69 / 833.0 | 0.0% | 0 |
| precomputation FROST (3, 5) | 0-RTT QUIC, 요청마다 | 20.88 / 202.02 / 221.67 / 1022.8 | 0.1% | 0 |
| FROST (3, 5) | TCP, 유지 | 40.58 / 269.88 / 479.97 / 600.7 | 0.0% | 0 |
| FROST (3, 5) | TCP, 요청마다 | 80.82 / 1144.55 / 2110.96 / 3506.1 | 22.2% | 0 |
| FROST (3, 5) | TCP + TLS, 요청마다 | 121.94 / 1205.92 / 2358.01 / 3106.4 | 20.6% | 1 |
| FROST (3, 5) | QUIC, 요청마다 | 82.17 / 281.86 / 345.23 / 1082.8 | 0.1% | 0 |
| FROST (3, 5) | 0-RTT QUIC, 요청마다 | 41.72 / 241.40 / 266.92 / 465.2 | 0.0% | 0 |
| threshold RSA (2, 5) | TCP, 유지 | 9.16 / 221.08 / 233.05 / 453.7 | 0.0% | 0 |
| threshold RSA (2, 5) | TCP, 요청마다 | 14.28 / 1030.26 / 1069.06 / 3438.0 | 6.9% | 0 |
| threshold RSA (2, 5) | TCP + TLS, 요청마다 | 19.96 / 1033.87 / 1077.75 / 2995.3 | 6.8% | 1 |
| threshold RSA (2, 5) | QUIC, 요청마다 | 15.83 / 56.77 / 216.05 / 234.3 | 0.0% | 0 |
| threshold RSA (2, 5) | 0-RTT QUIC, 요청마다 | 9.80 / 29.35 / 206.73 / 238.3 | 0.0% | 0 |

TCP 연결을 요청마다 맺으면 서명의 6.8~22.2%가 1초 넘게 늦었다. 연결 수립의 첫 packet(SYN)이나 그 응답이 손실되면 TCP는 1초 뒤에 다시 보낸다[28][29]. 한 서명이 여는 연결이 많을수록 이 비율이 높다. 연결 수는 threshold RSA (2, 5)에서 2개, precomputation FROST (3, 5)에서 3개, FROST (3, 5)에서 6개다.

최댓값이 3~4초인 서명도 있었다. SYN을 다시 보낸 연결에서는 data의 재전송 timer가 3초로 정해진다[28][29]. 1초와 3초의 대기가 이어진 경우로 본다. TLS를 쓰는 TCP의 세 조건에서 서명이 하나씩 실패했다. 실패한 서명은 모두 연결을 맺는 데 준 제한 시간 5초를 넘겼다. 이 제한 시간은 측정 도구가 정한 값이다.

QUIC 연결을 요청마다 맺으면 1초 넘게 늦은 서명은 0.1% 이하였다. quic-go는 RTT 표본이 없는 연결에서 probe timeout을 200 ms로 두므로, 첫 packet이 손실돼도 200 ms 뒤에 다시 보낸다. 0-RTT QUIC도 요청이 실린 첫 packet이 손실되면 같은 대기를 겪는다. precomputation FROST (3, 5)의 p95는 QUIC 203.6 ms, 0-RTT QUIC 202.0 ms였다.

같은 loss에서 유지되는 QUIC 연결의 p95는 42.7 ms(4절)다. 연결을 유지하는 쪽이 꼬리가 짧다.

### 6.3 0-RTT QUIC과 handshake가 몰릴 때

5절과 같은 방법으로 동시 handshake 100개를 측정했다. 0-RTT QUIC은 nudge를 쓰지 않으므로 같은 조건을 두 번 측정한 것이 되며, 두 값을 범위로 적었다. 8개 조건 모두에서 handshake 5,000개가 전부 성공했고 요청은 전부 0-RTT data로 전달됐다.

| (t, n) | loss | T_hs 중앙값 | T_hs p99 | D_sign 중앙값 | 비교: QUIC × 4의 T_hs 중앙값 |
| --- | --- | --- | --- | --- | --- |
| (2, 5) | 0% | 67.3~69.8 ms | 98.0~113.5 ms | 33.9~37.2 ms | 35.6 ms |
| (2, 5) | 2% | 68.3~69.3 ms | 257.6~260.2 ms | 33.9~36.2 ms | 35.8 ms |
| (3, 5) | 0% | 86.3~90.5 ms | 110.4~113.1 ms | 49.4~52.9 ms | 49.0 ms |
| (3, 5) | 2% | 89.7~91.7 ms | 277.3~278.7 ms | 56.3~57.1 ms | 49.6 ms |

loss가 없어도 T_hs 중앙값이 QUIC × 4보다 32~42 ms 길었다. handshake 100개가 한 번에 오면 coordinator는 signer마다 QUIC 연결 100개를 동시에 연다. 늘어난 시간이 coordinator와 signer 중 어디에서 쓰였는지는 나누어 측정하지 않았다. loss 2%에서는 4.4~6.4%의 handshake가 100 ms 넘게 늦었다.

## 7. 해석

coordinator와 signer 사이 transport가 서명 지연에 더하는 시간은 세 곳에서 나왔고, TCP와 QUIC의 차이는 각 곳의 상수와 구조에 있다.

| 지연이 생기는 곳 | TCP (Linux) | QUIC (quic-go) | 줄이는 방법 |
| --- | --- | --- | --- |
| 뒤따르는 packet이 없는 연결에서 손실을 알아채는 timer | 2r + 206~213 ms | 2r + 27 ms | TCP는 ping nudge. QUIC은 그대로 두거나, 가까운 signer에 한해 nudge |
| 연결 하나에 몰린 요청 | 손실에 줄어든 congestion window와 연결 내 전달 순서. 몫은 나누지 못함 | 손실에 줄어든 congestion window | signer당 연결 여러 개. 측정에서는 TCP 64개, QUIC 4개 |
| 연결 수립 | 요청마다 1~2 왕복이 더해지고, 첫 packet 손실에 1초 | 요청마다 1 왕복이 더해지고, 첫 packet 손실에 200 ms. 0-RTT는 왕복이 더해지지 않음 | 연결 유지 |

세 지연 모두 threshold signature의 성질이 아니라 transport의 성질이다. threshold signature는 이 지연이 드러날 기회를 늘린다. 서명 하나가 signer t개의 응답을 round마다 모두 기다리므로, 요청이나 응답 하나가 손실될 확률이 signer 수와 round 수에 따라 커진다. 6.2절에서 한 서명이 여는 연결이 2, 3, 6개로 늘 때 1초 넘게 늦은 서명이 6.9%, 11.7%, 22.2%로 늘어난 것이 그 예다.

주제 원문의 "하위 서버 간 핸드셰이크 지연시간"을 coordinator와 signer 사이 연결 수립의 비용으로 읽으면, 이 측정의 답은 다음과 같다. 연결을 유지하면 그 비용은 client가 겪는 handshake에 들어가지 않는다. 요청마다 연결을 맺으면 "(요청 하나의 왕복 수 − 1) × round 수 × 최대 RTT_s"가 T_hs에 더해지고, loss가 있으면 연결 수립 packet의 재전송 timer가 꼬리를 정한다. 0-RTT는 loss가 없고 요청이 하나씩 올 때만 유지되는 연결과 같았다.

## 8. 한계

- qlog는 coordinator 쪽 QUIC 연결에서만 기록했다. signer 쪽의 전송과 TCP의 동작(재전송 timer, congestion window)은 packet 단위로 확인하지 않았다. TCP에 관한 설명은 Linux kernel의 상수와 측정값의 일치에 근거한다.
- QUIC 구현체는 quic-go 하나다. congestion control, 초기 congestion window, probe timeout의 초깃값은 구현체마다 다르다. 결과는 Linux kernel 7.0의 TCP 기본 설정과 quic-go의 기본 설정에서 얻었다.
- loss는 packet마다 독립인 모형이고 비율은 2% 하나다. 이 loss는 혼잡과 무관한데 congestion control은 window를 줄였다. 실제 혼잡에서 생기는 loss에서는 연결을 늘려 전송량을 유지하는 것이 혼잡을 키울 수 있다.
- signer와 coordinator의 상호 인증을 구현하지 않았다. signer 인증서의 chain을 검증하지 않았다. 0-RTT data의 재전송 공격에 관한 안전성은 검토하지 않았다.
- 동시 handshake 수는 100 하나, QUIC 연결 수는 1, 4, 16 세 값이다. 연결 수를 동시 요청 수와 RTT_s에 맞춰 정하는 규칙은 만들지 않았다. 연결 2개와 3개는 측정하지 않았다.
- 유지되는 TLS 연결은 요청을 하나씩 보내는 조건에서만 측정했다. 요청마다 연결을 맺는 transport 중 동시 handshake로 측정한 것은 0-RTT QUIC뿐이다.
- 0-RTT QUIC이 handshake가 몰릴 때 느린 원인을 분해하지 않았다.
- 6.2절의 실패 3건은 측정 도구가 정한 연결 제한 시간 5초의 결과다.
- handshake 측정의 서버는 quic-go 하나이고, host에서 실험과 무관한 프로세스가 CPU를 쓰고 있었다.

## 9. 설계에 주는 영향

- coordinator와 signer 사이 transport의 기본 구성을 "signer당 유지되는 QUIC 연결 4개"로 바꾼다. 이 구성은 nudge 없이 loss 2%가 T_hs 중앙값에 더한 지연이 0.6 ms 이하였고, p99가 TCP × 64 + nudge보다 짧았다.
- TCP를 써야 하는 환경에서는 "signer당 연결 여러 개 + ping nudge"를 유지한다.
- nudge는 TCP에서는 필수이고 QUIC에서는 선택이다. QUIC에서는 nudge의 지연 시간이 r + 27 ms보다 짧은 signer에서만 꼬리를 줄인다.
- 요청마다 연결을 맺는 구성은 권하지 않는다. 연결을 유지할 수 없으면 0-RTT QUIC이 유지되는 연결에 가장 가깝지만, handshake가 몰리거나 loss가 있으면 차이가 크다.
- 지연 모델의 loss 항을 transport별 상수로 적는다. 뒤따르는 packet이 없는 연결에서 손실 하나가 더하는 시간은 r + c이고, c는 Linux TCP에서 206~213 ms, quic-go에서 27 ms다. 연결 하나에 몰린 요청의 지연은 congestion window로 설명하고, 연결 수를 결정 변수로 둔다.
- 다음 측정 후보는 QUIC 연결 수를 동시 요청 수와 RTT_s에 맞춰 정하는 규칙, signer 장애와 RTT_s 변동 조건, Phase 3의 최적화 문제다.
