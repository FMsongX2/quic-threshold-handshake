# 결과 종합

이 문서는 결과 문서들의 결론을 연구 질문별로 모은다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절을 따른다. 각 수치의 조건과 한계는 인용한 문서에 있다. 여기의 수치는 모두 한 host의 emulation 테스트베드에서 잰 값이다.

기호는 다음과 같다. T_hs는 client가 잰 handshake 시간, D_sign은 front server가 서명을 요청해 받기까지의 시간, RTT_c는 client와 front server 사이의 왕복 시간, RTT_s는 front server와 signer 사이의 왕복 시간이다. (t, n)은 signer n개 가운데 t개가 협력해야 서명이 만들어지는 구성이다.

## 1. 측정한 범위

| 항목 | 측정한 것 |
| --- | --- |
| 서버 구현체 | quic-go, aioquic, Cloudflare quiche, s2n-quic |
| client 구현체 | quic-go, s2n-quic |
| signature scheme | FROST(Ed25519), Shoup threshold RSA-2048, threshold ECDSA P-256(GG20). 비교 기준으로 원격 signer 하나의 Ed25519와 RSA |
| (t, n) | (1, 1), (2, 3), (3, 5), (2, 5), (4, 5) |
| signer 선택 정책 | fixed, fastest, first, hedge, 지연 hedge, hedge 예산, ping nudge |
| signer 연결 | TCP, TLS 1.3, QUIC. 유지하는 연결과 요청마다 맺는 연결, 연결 수 1~64 |
| 네트워크 | RTT_c 1·20·100 ms, RTT_s 0~200 ms, 두 link의 loss 0~5%, RTT_s의 jitter |
| 시스템 | front server의 CPU 1·2·4개, signer의 CPU 1·2개, 동시 handshake 1~300개, 초당 20~1,600개의 rate |
| 장애 | signer 하나를 죽임, 멈춤, 느리게 함 |

## 2. 연구 질문별 결론

### RQ1. 구현체는 서명을 기다리는 동안 무엇을 하는가

서명을 기다리는 방식은 구현체마다 달랐고, 그 차이는 handshake가 겹칠 때만 T_hs에 나타났다.

| 서버 | 서명 hook | 서명을 기다리는 동안 | 지연 50 ms, 동시 100개의 T_hs 중앙값 | 처리율의 상한 |
| --- | --- | --- | --- | --- |
| quic-go | 동기형. 연결마다 goroutine | client에 아무것도 보내지 않는다. | 74 ms | 초당 1,600개까지 없음 |
| quiche | 비동기형 | ACK와 ServerHello를 먼저 보낸다. | 73 ms | 초당 1,600개에서 T_hs가 2.5~3.3 ms 늘어남 |
| s2n-quic | 비동기형 | ACK를 먼저 보낸다. | 72 ms | 초당 1,600개까지 없음 |
| aioquic | 동기형. event loop 하나 | ACK를 먼저 보내고, 서명하는 동안 다른 연결을 처리하지 못한다. | 3,228 ms | "1 / (지연 + 1.6 ms)". 지연 20 ms에서 초당 47개 |

- 동기형이어도 연결마다 실행 단위가 따로 있으면(quic-go) 서명 지연이 다른 handshake를 막지 않았다. 가르는 것은 hook의 종류가 아니라 실행 단위를 공유하는지다.
- coordinator를 서버 밖의 daemon으로 두는 비용은 동시 handshake 100개에서 0.4~3.3 ms였다. TLS library가 서명을 밖으로 넘길 수 있으면 서버 구현체를 고치지 않고 threshold signing을 붙일 수 있다.

근거: [results-phase1-pilot.md](results-phase1-pilot.md), [results-phase1-grid.md](results-phase1-grid.md) 5절, [results-phase1-s2n.md](results-phase1-s2n.md) 3절, [results-phase2-implementations.md](results-phase2-implementations.md).

### RQ2. 서명 지연은 무엇으로 이루어지는가

D_sign의 중앙값은 "요청 round 수 × t번째로 가까운 signer의 RTT_s + signer의 계산 시간"이었다.

| scheme | 요청 round | signer의 계산 | RTT_s 5 ms, (3, 5)의 D_sign 중앙값 | signer의 CPU 하나가 지속하는 서명 rate |
| --- | --- | --- | --- | --- |
| precomputation FROST | 1 | 0.1~0.3 ms | 5.5 ms | 초당 1,600개에서 밀리지 않음 |
| FROST | 2 | 0.1~0.3 ms | 10.8 ms | 측정하지 않음 |
| threshold RSA | 1 | 4.9 ms | 10.3~12.2 ms | 초당 약 200개 |
| precomputation threshold ECDSA | 1 | 14~36 ms | 41.0 ms | 초당 2~4개 |
| threshold ECDSA | 6 | 222~697 ms | 529.9 ms | 측정하지 않음 |

- signer link에 loss가 있으면 손실을 겪은 응답이 "2 × RTT_s + c" 뒤에 왔다. c는 QUIC에서 27 ms, TCP에서 약 210 ms다. 서명이 손실을 겪을 확률은 round 수와 기다리는 signer 수에 따라 는다.
- handshake가 겹치면 signer의 계산 대기가 D_sign을 정했다. threshold RSA (3, 5)의 D_sign 중앙값은 동시 handshake 1, 10, 100개에서 9.6, 45.1, 375.4 ms였다.
- threshold ECDSA의 precomputation은 서명 지연을 줄이지만 presignature 하나에 signer의 CPU 시간이 0.23~0.45초 든다. presignature는 signer 집합에 묶여 있어, 집합이 바뀌면 그 효과가 사라지고 handshake가 실패하기도 했다.
- signer 연결은 유지하는 QUIC 연결 4개로 충분했다. 동시 handshake 300개, loss 5%, RTT_s 50 ms까지 확인했다. TCP는 연결 16개와 nudge가 함께 있어야 같은 결과가 났다.

근거: [results-phase2-signer.md](results-phase2-signer.md), [results-phase2-environment.md](results-phase2-environment.md), [results-phase2-ecdsa.md](results-phase2-ecdsa.md), [results-phase3-signer-transport.md](results-phase3-signer-transport.md), [results-phase3-robustness.md](results-phase3-robustness.md) 3절.

### RQ3. 서명 지연은 handshake 시간으로 어떻게 전달되는가

handshake를 하나씩 할 때 T_hs 중앙값은 "RTT_c + D_sign + 약 1 ms"였다. 단순 덧셈보다 커지는 조건은 세 가지였다.

- 덧셈이 성립한 범위: 주입한 지연 0~1,200 ms, RTT_c 1·20·100 ms, 서버 구현체 4개, client 구현체 2개에서 차이가 1 ms 안이었다. 실제 signer를 붙인 72개 조건에서 T_hs에서 D_sign과 RTT_c를 뺀 값은 0.75~1.27 ms였다. D_sign이 client의 probe timeout보다 길면 client가 probe를 보냈지만 T_hs는 늘지 않았다.
- 커지는 조건 1, 실행 단위를 공유하는 서버: aioquic은 D_sign 50 ms, 동시 handshake 10개에서 T_hs 중앙값이 283~290 ms였다(다른 서버 71~74 ms).
- 커지는 조건 2, signer의 계산 대기: threshold RSA는 동시 handshake 100개에서 T_hs가 0.4~0.6초였다. 서버 구현체와 무관했다.
- 커지는 조건 3, client link의 loss: 서명 지연과 별개로 구현체의 손실 복구가 꼬리를 정했다. loss 2%, RTT_c 20 ms에서 늦어지는 크기는 quiche 약 60 ms, quic-go 약 200 ms, s2n-quic 약 1초였다. RTT 표본을 얻기 전에 기다리는 시간의 초깃값이 구현체마다 다르기 때문이다.
- front server의 CPU 수는 동시 handshake 100개에서만 나타났다. T_hs에서 D_sign을 뺀 값이 handshake를 하나씩 할 때보다 CPU 1, 2, 4개에서 7.0~10.3, 3.5~4.9, 1.6~2.8 ms 길었다.

근거: [results-phase1-pilot.md](results-phase1-pilot.md), [results-phase1-grid.md](results-phase1-grid.md), [results-phase1-s2n.md](results-phase1-s2n.md), [results-phase2-environment.md](results-phase2-environment.md), [results-phase2-implementations.md](results-phase2-implementations.md).

### RQ4. 어떤 구성이 지연을 가장 낮추는가

signer 5개를 RTT_s가 5, 20, 80 ms인 세 region에 두고 정책을 전수 측정했다. signer link의 loss는 2%다.

| 보안 조건 | 구성 | D_sign 중앙값 | D_sign p99 |
| --- | --- | --- | --- |
| t = 2. region 하나의 침해로 key가 넘어간다. | 5/5/5/20/80, hedge4 | 5.6 ms | 9.5 ms |
| t = 3. region 하나의 침해로 key가 넘어간다. | 5/5/5/20/80, hedge5 | 6.6 ms | 22.0 ms |
| t = 3. region 하나가 침해되어도 안전하고, region 하나가 사라져도 서명한다. | 5/5/20/20/80, hedge4 | 20.7 ms | 24.7 ms |
| 위와 같은 조건, threshold RSA | 5/5/20/20/80, first + nudge | 24.2 ms | 26.4 ms |
| t = 4. region 하나가 사라지면 서명하지 못한다. | 5/5/5/20/80, fastest + nudge | 20.6 ms | 74.0 ms |

- 중앙값은 t번째로 가까운 signer의 RTT_s가 정한다. 정책에 따른 차이는 1.2 ms 안이었다.
- 꼬리는 여유 signer의 위치가 정한다. 가장 낮은 p99는 "(t + 1)번째로 가까운 signer의 RTT_s"와 "2 × t번째 signer의 RTT_s + 27 ms" 가운데 작은 값에 가까웠다.
- region 안전성의 비용은 중앙값이다. 가장 가까운 region의 RTT_s(5 ms)에서 두 번째로 가까운 region의 RTT_s(20 ms)로 오른다.
- 정책을 고르지 않고 fastest만 쓰면 region 안전한 구성의 p99가 85.6 ms다. hedge4는 share를 4배 계산하고 이를 24.7 ms로 줄였다.
- signer 하나가 응답하지 않으면 fixed와 fastest는 handshake가 모두 실패했다. hedge, 지연 hedge, first는 모두 성공했고 T_hs가 다음으로 가까운 signer의 RTT_s만큼 늘었다. hedge 예산 0.2는 장애 때 성공률을 21%로 떨어뜨렸다.

근거: [results-phase3-optimization.md](results-phase3-optimization.md), [results-phase3-robustness.md](results-phase3-robustness.md) 4절, [results-phase3-hedging.md](results-phase3-hedging.md), [results-phase3-adaptive.md](results-phase3-adaptive.md).

## 3. 가설의 판정

| 가설 | 판정 | 근거 |
| --- | --- | --- |
| H1. 실행 단위를 공유하는 동기형 hook 구현체에서는 동시 handshake 수에 비례해 T_hs가 늘고, 비동기형에서는 유지된다. | 맞음. 동기형이어도 실행 단위를 공유하지 않는 quic-go는 유지됐다. | 지연 50 ms에서 동시 1·10·100개의 중앙값이 aioquic 71.6·277.2·3,228.4 ms, quiche 71.2·71.6·72.7 ms, s2n-quic 72.0·72.1·72.3 ms |
| H2. RTT_s가 수십 ms 이상이면 D_sign은 왕복 횟수에 좌우된다. precomputation을 켠 FROST의 증가량은 켜지 않은 것의 절반이다. | 맞음. "RTT_s가 1 ms 미만이면 계산 시간에 좌우된다"는 scheme에 따라 달랐다. | RTT_s 0 → 50 ms에서 증가량이 FROST 100.1 ms, precomputation FROST 50.0 ms, threshold RSA 50.9 ms |
| H3. D_sign이 client의 PTO보다 짧으면 T_hs는 D_sign만큼 늘고, 길면 증가량이 D_sign과 달라진다. | 앞부분 맞음, 뒷부분 기각. PTO보다 길어도 증가량은 D_sign과 1 ms 안에서 같았다. | 지연 0~1,200 ms, RTT_c 1·20·100 ms, 서버 4개, client 2개 |
| H4. p99를 최소화하는 구성은 signer 배치와 선택 정책으로 결정되고, order statistic 모델이 실측 순위를 재현한다. | 앞부분 맞음, 뒷부분 일부만 맞음. 미리 정한 판정 기준은 충족했으나 기준이 너무 넓었다. | 모델의 1순위가 실측 최저 정책과 일치한 묶음이 24개 중 11개. 중앙값의 오차는 2.6 ms 이내, loss가 있을 때의 p99는 132개 조건 중 52개에서 10 ms 넘게 낮게 잡음 |

## 4. 권고 구성

측정한 범위 안에서 다음 구성을 권한다. 실제 배치의 근거로 쓰려면 5절의 한계를 먼저 풀어야 한다.

1. 서버 구현체는 서명을 기다리는 동안 다른 연결을 처리할 수 있어야 한다. quic-go, quiche, s2n-quic이 해당한다. loss가 있는 client link에서는 quiche의 꼬리가 가장 짧았다.
2. coordinator와 signer 사이는 signer당 QUIC 연결 4개를 유지한다.
3. scheme은 PKI에 따라 고른다. 사설 PKI이면 precomputation을 쓰는 FROST가 가장 빠르다. 공개 WebPKI이면 threshold RSA를 쓰고, signer의 CPU 하나당 초당 약 200개인 서명 rate를 handshake rate의 상한으로 잡는다. 측정한 threshold ECDSA(GG20)는 서명 rate와 집합 전환 비용 때문에 권하지 않는다.
4. signer 5개를 세 region에 둔다면 t = 3, region별 signer 수 2, 2, 1로 두고 가까운 두 region에 2개씩 둔다. 정책은 FROST에서 가까운 4개의 hedge, threshold RSA에서 first다.
5. fixed와 fastest를 그대로 쓰지 않는다. signer 하나의 장애로 모든 handshake가 실패한다. hedge 예산은 장애가 의심될 때 풀어야 한다.

## 5. 예측과 달랐던 것

측정 전에 적은 예측 가운데 틀린 것과, 예측에 없던 결과다.

| 예측 | 실제 |
| --- | --- |
| D_sign이 PTO보다 길면 T_hs의 증가량이 D_sign과 달라진다. | 1 ms 안에서 같았다. |
| loss가 있을 때 필요한 QUIC 연결 수는 동시 100개에서 4~5개, 300개에서 13개다. | 2개와 4개였다. |
| signer의 CPU가 2개이면 처리율이 2배다. | 1.5배였다. signer들의 CPU가 물리 core를 나누어 썼다. |
| signer가 죽으면 TCP에서는 곧바로 실패한다. | 다시 연결하는 시도가 5초를 채운 뒤 실패했다. |
| hedge는 signer의 RTT_s 변화에 영향받지 않는다. | 세 번째로 가까운 signer가 바뀌어 30 ms 늘었다. |
| 여유 signer가 멀면 지연 hedge가 가장 낮다. | hedge가 가장 낮았다. 모델이 지연 hedge를 잘못 계산했다. |
| fastest의 p99는 "2r + 27 ms"다. | p95가 그 값이었고 p99는 더 길었다. |
| 두 번째 client에서 quiche는 RTT_c의 3배쯤만 늦어진다. | 네 서버 모두 handshake의 1~3.5%가 약 1초 늦어졌다. |
| (예측 없음) | hedge 예산이 signer 장애 때 성공률을 21%로 떨어뜨렸다. |
| (예측 없음) | threshold ECDSA는 signer 집합이 바뀌면 handshake가 실패했다. |
| (예측 없음) | s2n-quic 서버는 손실된 ServerHello를 약 1초 뒤에 다시 보냈다. |
| (예측 없음) | 두 번째 client로 handshake 100개를 함께 시작하면 loss가 없어도 quiche 서버에서 49%가 약 1.09초 걸렸다. 원인은 확인하지 못했다. |

## 6. 한계

- 모든 container가 한 host에 있고, host에서 실험과 무관한 process가 CPU를 쓰고 있었다. signer 5개 가운데 둘은 같은 물리 core의 thread를 썼다.
- 링크는 `tc netem`의 지연과 서로 독립인 loss다. 실제 WAN에서 재지 않았다.
- p99는 조건마다 표본 1~50개로 정해진다. 같은 조건을 반복해 흔들림을 재지 않았다.
- Phase 3의 배치는 signer 5개, region 3개, RTT_s 5·20·80 ms 한 가지 조합이다.
- threshold ECDSA는 갱신이 멈춘 library의 GG20이다. 더 새로운 protocol은 재지 않았다.
- key는 dealer가 나누었다. 분산 key 생성, signer와 coordinator의 상호 인증, nonce commitment와 presignature의 장애 뒤 관리는 구현하지 않았다.
- hedge가 FROST의 동시 세션 안전성에 기대는 부분은 검토받지 않았다.
- 응답 없는 signer를 선택에서 빼고 다시 확인하는 기능이 없다. signer 둘 이상의 장애와 region 전체의 장애는 재지 않았다.
- 인증서 chain의 크기, session resumption, client 인증은 다루지 않았다.

## 7. 남은 일

- 응답 없는 signer의 감지와 복귀, 장애 때 hedge 예산을 푸는 규칙을 구현하고 측정한다.
- 모델에 QUIC의 긴 손실 복구와 TCP의 연쇄 지연을 넣고, 다른 배치로 "여유 signer" 규칙을 검증한다.
- threshold ECDSA를 CGGMP21이나 DKLs23 구현으로 다시 잰다.
- s2n-quic 서버가 손실된 ServerHello를 늦게 다시 보내는 원인과, 두 번째 client에서 handshake가 겹칠 때 1초가 걸리는 원인을 packet 단위로 확인한다.
- 대표 구성 하나를 실제 WAN에서 잰다.
