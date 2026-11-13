# 작업 기록

날짜별로 한 일, 내린 결정과 그 이유, 확인한 사실을 적는다. 측정 결과의 수치와 해석은 결과 문서에 두고 여기서는 위치만 가리킨다.

## 2026-11-10

- 주제를 연구 질문 4개(RQ1~RQ4)와 가설로 구체화하고 [problem-definition.md](problem-definition.md), [experiment-design.md](experiment-design.md), [related-work.md](related-work.md)를 작성했다.
- QUIC 구현체 15개의 source를 받아 CertificateVerify 서명 hook과 서명 대기 처리 방식을 조사했다. TLS 라이브러리가 비동기 hook을 제공해도 QUIC 구현체가 이를 처리하지 않는 경우(picoquic, lsquic, xquic)가 있어, 구현체 단위로 분류했다([related-work.md](related-work.md) 5.2절).
- 초기 서버 구현체를 Cloudflare quiche·s2n-quic(비동기 서명)과 quic-go·aioquic(동기 서명)으로 정했다.

## 2026-11-13

### 실험 환경

- 실험 host를 Docker를 쓸 수 있는 Linux host 1대로 정했다. container 안에서 `tc netem`이 동작하는 것을 확인했다.
- QUIC Interop Runner 대신 container마다 `tc netem`을 거는 구성을 쓰기로 했다. 서명 hook을 바꾼 서버를 직접 build해야 하고, Interop Runner가 요구하는 Wireshark 4.5 이상이 host에 없기 때문이다.

### Phase 1: 서명 지연 주입

- `harness/`에 quic-go client, 서명 지연 서버 3종(quic-go, aioquic, Cloudflare quiche), 실행 스크립트 `run.py`, 집계 스크립트 `analyze.py`를 만들었다.
- 지연 sweep, 동시 handshake, loss 5% 조건을 측정했다. 결과와 가설 판정은 [results-phase1-pilot.md](results-phase1-pilot.md)에 있다.
- 이 결과로 정한 것: 단일 handshake의 T_hs는 지연 없는 handshake 시간과 서명 지연의 합으로 모델링한다. Phase 1의 나머지 격자는 줄이고 Phase 2로 넘어간다.

### Phase 2: threshold signer 구현

library 선정은 다음과 같다.

| 용도 | library | version | 선정 이유 |
| --- | --- | --- | --- |
| FROST(Ed25519, SHA-512) | `github.com/bytemare/frost` | commit 316cadd (2026-06-16) | RFC 9591을 구현하고 Ed25519 ciphersuite를 지원한다. commitment에 ID가 있어 precomputation을 그대로 쓸 수 있다. MIT license다. tag가 붙은 release는 없다. |
| Shoup threshold RSA | `github.com/cloudflare/circl` `tss/rsa` | v1.6.5 | RSA-PSS padding을 지원한다. BSD-3-Clause license다. |

구현에서 내린 결정은 다음과 같다.

- signer 서비스와 coordinator를 Go package `harness/internal/tsig`로 만들었다. coordinator는 `crypto.Signer`를 구현하므로 quic-go 서버가 인증서 key로 그대로 쓴다.
- coordinator와 signer 사이는 TCP 위의 길이 prefix frame으로 통신한다. TLS는 쓰지 않았다. 연결을 유지하는 방식과 요청마다 새로 맺는 방식을 모두 지원한다.
- key share는 trusted dealer가 만든다. dealer는 전체 key로 인증서에 자체 서명한 뒤 share만 저장한다.
- RSA key는 Go 표준 library로 만든 2048 bit key다. safe prime이 아니다. 인증 기관이 발급한 기존 인증서의 key가 대개 safe prime이 아니라는 점([related-work.md](related-work.md)의 참고문헌 9)에 맞춘 선택이며, Shoup 방식의 안전성 증명이 요구하는 조건은 충족하지 않는다.
- RSA-PSS의 salt는 coordinator가 정하고 padding한 값을 signer에게 보낸다. signer마다 따로 padding하면 share를 결합할 수 없기 때문이다.
- (t, n) = (1, 1) 비교 기준으로, 전체 key를 가진 signer 하나가 서명하는 plain 방식을 Ed25519와 RSA에 대해 넣었다.
- signer 선택 정책은 고정(설정 순서의 앞 t개), RTT 추정이 가장 짧은 t개, 전체에 요청 후 먼저 온 t개(threshold RSA 전용) 세 가지다.

구현 중 확인한 사실은 다음과 같다.

- `bytemare/frost`는 ciphersuite마다 hasher 하나를 process 전체가 공유한다. 한 process에서 FROST 함수를 동시에 호출하면 SHA-512 내부 상태가 깨져 panic이 났다(`d.nx != 0`). `tsig`는 이 library 호출 전체를 mutex 하나로 직렬화한다. 따라서 coordinator process 안에서 FROST 서명 결합은 한 번에 하나씩 처리된다.
- FROST로 만든 서명은 Go 표준 library의 `ed25519.Verify`를, threshold RSA로 만든 서명은 `rsa.VerifyPSS`를 통과했다(`harness/internal/tsig/tsig_test.go`).
- quic-go 서버에 coordinator를 붙여 FROST Ed25519 인증서와 threshold RSA 인증서로 QUIC handshake가 성공했다. client는 quic-go다.

측정 구성에서 내린 결정은 다음과 같다.

- signer마다 container를 하나씩 띄우고 signer 전용 bridge network에 연결한다. 실행 스크립트는 `harness/run_sign.py`, 집계 스크립트는 `harness/analyze_sign.py`다.
- coordinator와 signer 사이의 RTT는 signer container의 송신 방향에만 왕복 지연 전체를 걸어 만든다. coordinator는 signer마다 다른 지연을 한 interface에서 걸어야 해서 설정이 복잡해지기 때문이다. 요청과 응답 한 쌍의 왕복 시간은 양쪽에 절반씩 건 경우와 같다. loss는 양쪽 송신 방향에 건다.

### Phase 2: 측정

실행한 측정과 결과의 위치는 다음과 같다. 결과는 [results-phase2-signer.md](results-phase2-signer.md)에 정리했다.

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 서명 지연 분해 | `results/2026-11-13-phase2-dsign` | scheme 8종 × RTT_s {0, 5, 50} ms × transport 2종, 조건마다 200회 |
| signer 배치와 선택 정책 | `results/2026-11-13-phase2-policy` | signer 5개의 RTT_s가 50·5·100·1·20 ms인 배치, 정책 3종, loss {0, 2}%, 조건마다 400회 |
| handshake | `results/2026-11-13-phase2-handshake` | quic-go 서버에 coordinator를 붙여 scheme 5종 × RTT_s {5, 50} ms, 조건마다 50회 |
| 동시 handshake | `results/2026-11-13-phase2-handshake-conc` | 동시 handshake 100개, scheme 4종, RTT_s 5 ms, 조건마다 500회 |

측정에서 확인한 사실과 그에 따른 결정은 다음과 같다.

- 가설 H2의 예측이 맞았다. D_sign에서 RTT_s에 비례하는 부분은 왕복 수와 RTT_s의 곱이었다.
- 실제 signer를 붙인 handshake에서도 T_hs는 지연 없는 handshake 시간과 D_sign의 합이었다. Phase 1에서 정한 모델을 유지한다.
- 동시 handshake 100개에서 threshold RSA의 D_sign 중앙값이 9 ms에서 258 ms로 늘었다. signer가 share 하나에 4~5 ms를 쓰고 CPU 하나에 고정돼 있어 요청이 줄을 서기 때문이다. signer의 처리 용량을 종속 변인으로 다뤄야 한다.
- 측정 중 host의 다른 프로세스가 CPU 9번을 계속 100% 쓰고 있었고 virtual machine의 thread가 여러 CPU에 걸쳐 있었다. frost (3, 5) 조건에서 세 번째 signer의 응답이 0.5~0.8 ms 늦게 관측됐는데 이 영향일 수 있다. RTT_s 0 ms 조건의 1 ms 미만 차이는 이 잡음보다 작으므로 비교에 쓰지 않는다.
- signer마다 RTT_s가 다른 배치에서 loss가 없을 때의 D_sign 중앙값은 "왕복 수 × 선택된 signer 중 가장 큰 RTT_s"에 계산 시간을 더한 값과 맞았다. 이 식을 모델의 네트워크 항으로 쓴다.
- signer 링크의 loss 2%에서, 선택한 signer 전부를 기다리는 구성은 서명의 5~21%가 100 ms 넘게 늦어졌다. 그 비율은 1 − 0.98^(2Rt)와 4%p 안에서 맞았다. 모델에 loss 항으로 넣는다.
- threshold RSA에 first 정책(전체에 요청, 먼저 온 t개 사용)을 쓰면 같은 loss에서 800회 중 1회만 늦어졌다. FROST는 share가 signer 집합에 묶여 이 정책을 쓸 수 없다. FROST에서 같은 효과를 내는 방법을 Phase 3의 후보로 둔다.
- 이동 평균으로 가장 빠른 signer를 고르는 fastest 정책은 loss가 있을 때 먼 signer에 머무는 경우가 있었다. 재전송으로 늦어진 응답 하나가 평균을 크게 올리고, 선택되지 않은 signer의 평균은 갱신되지 않기 때문이다. 이 정책은 실험용 구현이므로 결과를 "가장 빠른 signer를 고르는 정책 일반"의 성능으로 읽지 않는다.

### Phase 3 준비: FROST hedging

Phase 2에서 FROST는 선택한 signer 전부의 응답을 기다려야 해서 signer 링크의 loss에 약했다. threshold RSA의 first 정책처럼 "먼저 응답한 t개로 서명을 완성"하는 동작을 FROST에서 얻는 방법을 구현했다.

구현에서 내린 결정은 다음과 같다.

- 정책 이름은 hedge다. RTT 추정이 가장 짧은 m개 signer를 고르고, 그 m개에서 뽑을 수 있는 t개 조합마다 FROST 세션을 하나씩 진행해 먼저 끝난 세션의 서명을 쓴다. m은 t부터 n까지 설정할 수 있다.
- FROST의 signature share는 한 세션의 commitment 목록에 묶이므로 세션 사이에 share를 섞을 수 없다. 세션마다 각 signer의 서로 다른 nonce commitment를 쓴다. 같은 nonce로 두 번 서명하지 않는다.
- signer 하나가 속한 세션들의 서명 요청은 요청 하나에 묶어 보낸다. 세션마다 따로 보내도 같은 TCP 연결에서는 packet 하나의 손실이 뒤의 요청을 모두 늦추므로 이득이 없다. 묶어 보내면 먼저 응답한 t개 signer가 한 세션을 완성한다.
- precomputation을 쓰지 않을 때는 commitment 응답이 도착하는 대로, 구성원 전원의 commitment가 모인 세션부터 서명 요청을 보낸다.
- 비용은 signer가 계산하는 share 수와 소비하는 nonce commitment 수다. signer 하나가 서명 하나에 계산하는 share는 최대 C(m−1, t−1)개다. (t, m) = (2, 5)이면 4개, (3, 5)이면 6개다.
- 이기지 못한 세션의 nonce는 signer가 서명 요청을 받으면 지워진다. 요청이 signer에 도달하기 전에 취소되면 signer의 memory에 남는다. 이 실험에서는 정리하지 않았다.
- 보안 근거는 FROST가 동시 세션에서 안전하다는 성질이다. 이 구성의 안전성을 따로 증명하거나 검토받지 않았다.

함께 바꾼 것은 다음과 같다.

- signer의 RTT 추정을 이동 평균에서 최근 8개 표본의 최솟값으로 바꿨다. Phase 2에서 재전송으로 늦어진 응답 하나가 가까운 signer를 먼 signer 뒤로 밀어내는 문제를 확인했기 때문이다. fastest 정책과 hedge 정책이 이 추정을 쓴다. `results/2026-11-13-phase2-policy`의 fastest 결과는 바꾸기 전 구현(commit 29a4cdf)으로 측정한 것이다.
- 선택되지 않은 signer의 추정은 여전히 갱신하지 않는다. RTT가 시간에 따라 변하는 조건은 다루지 않았다.
- 서명 요청의 wire 형식을 commitment 목록 여러 개를 싣는 형식으로 바꿨다. 세션이 하나인 경우도 같은 형식을 쓴다.

### Phase 3 준비: hedging 측정

실행한 측정과 결과의 위치는 다음과 같다. 결과는 [results-phase3-hedging.md](results-phase3-hedging.md)에 정리했다.

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 정책 비교 | `results/2026-11-13-phase3-hedge-policy` | FROST 4종과 threshold RSA 2종, 정책 fastest·hedge(3)·hedge(4)·hedge(5)·first, loss {0, 2}%, 조건마다 1,000회 |
| handshake | `results/2026-11-13-phase3-hedge-handshake` | quic-go 서버, precomputation FROST와 threshold RSA, loss {0, 2}%, 조건마다 300회 |
| 동시 handshake | `results/2026-11-13-phase3-hedge-conc` | 동시 handshake 100개, FROST 3종, loss 0%, 조건마다 500회 |

확인한 사실과 그에 따른 결정은 다음과 같다.

- hedge는 loss 2%에서 FROST의 p95를 200 ms대에서 20~51 ms로 줄였다. 100 ms 넘게 늦어진 서명의 비율은 "m개 중 손실 없이 응답한 signer가 t개 미만일 확률"과 맞았다. 이 식을 모델의 loss 항으로 쓴다.
- m = t + 1만으로 느린 서명의 비율이 0.5~0.7%로 줄었다. 폭을 넓히는 이득은 빠르게 줄고 share 계산량은 조합 수만큼 늘어난다.
- handshake 100개가 동시에 오는 조건에서 hedge는 T_hs 중앙값을 1.2~2.3배로 늘렸다. 보관한 commitment가 모자랐기 때문이다. 폭을 고정하는 정책은 부하가 높을 때 손해이므로, 부하와 loss에 따라 폭을 바꾸는 정책을 다음 후보로 둔다.
- RTT 추정을 최솟값으로 바꾼 뒤 fastest 정책은 loss 2%에서도 가까운 signer를 유지했다.
- precomputation hedge에서 약 10.7회(= 64 / 6)마다 서명이 5~6 ms 늦어졌다. coordinator가 commitment를 64개씩 보충하는 주기와 같다. 보충 단위를 줄이거나 보충을 서명 요청과 겹치지 않게 하는 것은 하지 않았다.

### 남은 일

- threshold ECDSA를 구현하지 않았다. 공개 WebPKI에 쓸 수 있는 scheme 중 측정한 것은 threshold RSA뿐이다.
- coordinator와 signer 사이 연결에 TLS를 넣지 않았다.
- s2n-quic 서명 지연 서버, 두 번째 client 구현체, RTT_c 1 ms와 100 ms 조건을 측정하지 않았다.
- aioquic과 quiche 서버에는 coordinator를 붙이지 않았다. 실제 signer를 붙인 handshake는 quic-go 서버에서만 측정했다.
- signer에 CPU를 여러 개 주거나 복제본을 둔 조건, signer 배치를 여러 가지로 바꾼 조건, RTT_s에 jitter를 준 조건을 측정하지 않았다.
- Phase 3의 최적화 문제를 풀지 않았다.
- loss가 있으면서 handshake가 몰리는 조건, signer 장애 조건, RTT_s가 변하는 조건에서 hedge를 측정하지 않았다.
- 부하와 loss에 따라 hedge의 폭을 바꾸는 정책을 구현하지 않았다.
