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

## 2026-11-14

### 적응형 hedge 구현

hedge의 폭을 고정하면 loss가 없고 handshake가 몰릴 때 손해였다([results-phase3-hedging.md](results-phase3-hedging.md) 5절). 필요할 때만 폭을 넓히는 방식을 구현했다.

- 지연 hedge: 가장 가까운 t개 signer로 세션 하나(primary)만 먼저 진행한다. 정해 둔 시간 안에 primary가 끝나지 않으면 나머지 세션을 모두 시작한다. primary의 signer가 오류를 내면 시간을 기다리지 않고 바로 넓힌다.
- 기다리는 시간은 "계수 × 왕복 수 × primary에서 가장 먼 signer의 RTT 추정 + 2 ms"다. 2 ms는 signer 계산 시간의 여유다. 계수는 실행 option이다.
- hedge 예산: 넓힐 수 있는 서명의 비율에 상한을 둔다. 서명 하나마다 예산만큼 token이 쌓이고 넓힐 때 token 하나를 쓴다. token은 10개까지 쌓인다. 부하로 응답이 늦어져 timer가 연달아 만료될 때 hedge가 부하를 더 키우는 것을 막기 위한 장치다. signer 오류로 넓힐 때는 예산을 쓰지 않는다.
- 넓힐 때 필요한 commitment는 그때 pool에서 꺼내고 모자라면 signer에게서 받는다. 넓히지 않은 서명은 commitment를 signer마다 하나만 쓴다.
- 실행 스크립트의 정책 표기는 `hedge[N][dF][bB]`다. N은 폭, F는 지연 계수, B는 예산이다. 예를 들어 `hedge4d1.5b0.2`는 폭 4, 계수 1.5, 예산 20%다.

### ping nudge 구현

지연 hedge의 trace에서, 넓혀진 서명의 80~97%를 새 세션이 아니라 처음의 세션이 완성했다는 것을 확인했다. 넓힐 때 같은 TCP 연결로 나간 요청이 손실된 packet의 재전송을 앞당긴 것으로 보고, share를 더 계산하지 않는 방법을 구현했다.

- 정해 둔 시간 안에 응답하지 않은 signer의 연결에 ping 요청을 하나 보낸다. 세션은 그대로 하나다.
- fixed, fastest, first 정책과 모든 signature scheme에 적용된다. 연결을 유지하는 transport에서만 동작한다.
- 지연 hedge와 nudge가 같은 지연 시간 계산을 쓴다. 실행 스크립트의 정책 표기는 `fastestn1.5`처럼 정책 이름 뒤에 `nF`를 붙인다.

### RTT 추정 수정

동시 handshake 100개와 loss 2% 조건을 처음 측정했을 때, fastest 정책이 반복 한 번 전체를 RTT_s 50 ms나 100 ms인 signer로 보내는 일이 있었다. 같은 연결에서 함께 지연된 요청 100개의 표본이 "최근 8개 표본" 창을 모두 채워 가까운 signer의 추정이 먼 signer보다 커졌기 때문이다. nudge를 켜면 ping 표본이 더해져 이 일이 더 잦았다.

- 추정을 5초 길이의 시간 창 두 개(현재 창과 직전 창)의 최솟값으로 바꿨다.
- 바꾸기 전 구현으로 측정한 동시 handshake 결과(5회 반복과 20회 반복)는 버렸고 저장소에 넣지 않았다. 바꾼 뒤 50회 반복으로 다시 측정했다.
- 요청을 하나씩 보내는 측정(`results/2026-11-14-adaptive-hedge-policy`)은 바꾸기 전 구현으로 했다. fastest와 nudge 조건에서 선택된 signer가 1,000회 내내 가장 가까운 t개였음을 확인하고 그대로 쓴다.

### 적응형 hedge와 nudge 측정

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 서명 지연 | `results/2026-11-14-adaptive-hedge-policy` | FROST 3종과 threshold RSA 2종, 정책 fastest·hedge·지연 hedge·nudge·first, loss {0, 2}%, 조건마다 1,000회 |
| 동시 handshake | `results/2026-11-14-adaptive-hedge-conc` | 동시 handshake 100개를 50회, precomputation FROST 2종, 정책 6종, loss {0, 2}%, 조건마다 5,000개 |

결과는 [results-phase3-adaptive.md](results-phase3-adaptive.md)에 정리했다. 확인한 사실과 그에 따른 결정은 다음과 같다.

- 요청을 하나씩 보낼 때 nudge와 지연 hedge는 loss 2%의 꼬리 지연을 없앴고 loss가 없을 때는 fastest와 같았다. nudge는 share 계산을 늘리지 않으므로 signer 선택의 기본값을 "가장 가까운 t개 + nudge"로 둔다.
- Phase 2부터 본 200 ms 안팎의 꼬리 지연은 threshold signature의 성질이 아니라 유지되는 TCP 연결에서 packet 하나짜리 요청과 응답이 손실될 때의 복구 지연이라고 결론 내렸다. packet 단위 확인은 하지 않았다.
- hedge 예산은 부하가 높을 때 넓혀지는 비율을 10~15%로 묶었고 commitment 부족을 86~92%에서 4% 이하로 줄였다.
- 동시 handshake 100개와 loss 2% 조건에서는 어느 방법도 fastest보다 일관되게 낫지 않았다. 요청들이 signer마다 하나인 연결을 함께 쓰는 것이 원인이라고 보고, signer 연결을 여러 개 두는 방식과 QUIC을 쓰는 방식을 다음 후보로 둔다.
- tmux session 이름을 `-t qth`처럼 주면 앞부분이 같은 다른 session에도 맞는다. 측정을 이어 붙이는 script에서는 `-t =이름`으로 정확히 지정한다.

### 결과 저장 방식

- 서명·handshake별 기록(`sign.jsonl`, `client.jsonl`, `server-trace.jsonl`)을 gzip으로 압축해 저장소에 넣기로 했다. 이번 측정까지 더하면 `results/`가 122 MB였고 압축 뒤 19 MB다. 이전 측정의 기록도 같은 방식으로 바꿨다. 집계 스크립트는 두 형식을 모두 읽는다.
- 압축하기 전에 push가 한 번 거부됐다. GitHub의 push protection이 `client.jsonl`에 기록된 QUIC connection ID 하나(client가 만든 무작위 16진수 문자열)를 credential 형식으로 판정했다. 그 값이 connection ID임을 확인했고, 해당 commit은 push하지 않고 압축한 내용으로 다시 만들었다.

### signer 연결 pool 구현

동시 handshake 100개와 loss 2% 조건의 지연이 signer마다 하나인 연결을 함께 쓰기 때문이라는 해석을 확인하려고, signer마다 연결을 여러 개 두는 transport를 구현했다.

- 연결을 유지하는 transport에 signer당 연결 수 K를 option으로 넣었다. 요청은 연결에 차례대로 돌아가며 배정한다. coordinator가 시작할 때 모든 연결을 미리 맺는다.
- nudge의 ping은 그 요청을 실은 연결로 보낸다. 다른 연결로 보내면 손실된 packet의 재전송을 앞당기지 못하기 때문이다.
- 지연 hedge는 넓힐 때의 요청을 다음 차례의 연결로 보낸다. 연결이 여러 개이면 [results-phase3-adaptive.md](results-phase3-adaptive.md) 3.3절에서 본 재전송 효과가 없을 것으로 예상하며, 이 조합은 측정하지 않았다.
- 예상: 연결이 많을수록 손실 하나가 늦추는 요청의 수는 줄지만, 연결마다 지나가는 packet이 적어져 재전송이 약 200 ms의 timer를 기다리는 일이 다시 늘어난다. 따라서 연결을 늘릴 때는 nudge가 함께 필요하다.
- 실행 스크립트의 transport 표기는 `persistent16`처럼 뒤에 연결 수를 붙인다.

### signer 연결 pool 측정

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 동시 handshake | `results/2026-11-14-conn-pool-conc` | 동시 handshake 100개를 50회, precomputation FROST 2종, signer당 연결 수 {1, 4, 16, 64}, nudge 유무, loss {0, 2}%, 조건마다 5,000개 |
| 서명 지연 | `results/2026-11-14-conn-pool-policy` | 요청을 하나씩, 연결 수 {1, 4}, nudge 유무, loss {0, 2}%, 조건마다 1,000회 |

결과는 [results-phase3-connections.md](results-phase3-connections.md)에 정리했다.

- 측정 전의 예측이 맞았다. 연결을 16개로 늘리면 loss 2%에서의 T_hs 중앙값이 loss가 없을 때의 값으로 돌아왔고, nudge 없이 연결만 늘리면 100 ms 넘게 늦은 handshake가 0.3%에서 7.3%까지 늘었다. 연결 여러 개와 nudge를 함께 쓰면 둘 다 해결됐다.
- signer 링크의 loss가 handshake를 늦추는 경로를 둘로 정리했다. 꼬리는 손실된 packet 뒤에 다른 packet이 없을 때의 재전송 대기이고 대책은 nudge다. 중앙값은 한 연결에 몰린 요청이 손실 하나를 함께 기다리는 것이고 대책은 연결 여러 개다.
- coordinator와 signer 사이 transport의 기본 구성을 "signer당 연결 여러 개 + ping nudge"로 정했다.
- 첫 실행은 연결 64개 조건에서 서버가 준비 시간 20초를 넘겨 중단됐다. coordinator가 연결을 하나씩 순서대로 맺었기 때문이다. 연결을 한꺼번에 맺도록 고치고 준비 대기 시간을 60초로 늘린 뒤 동시 handshake 측정 전체를 다시 했다. 중단된 실행의 결과는 쓰지 않았다.

## 2026-11-17

### signer 연결의 TLS와 QUIC 구현

signer 연결을 여러 개 두는 것은 요청마다 전달 순서를 독립시키는 수단이었다. QUIC은 연결 하나 안에서 stream마다 전달 순서가 독립이므로, signer 연결에 QUIC을 쓰는 transport를 구현해 비교한다. 지금까지의 signer 연결은 암호화가 없는 TCP여서 QUIC과 조건이 다르다. 같은 조건의 비교 대상으로 TLS 1.3을 쓰는 TCP transport도 함께 넣었다.

| transport | 연결 | 요청 하나의 전달 |
| --- | --- | --- |
| `persistent` | TCP, 유지 | 연결 하나에 요청 ID로 다중화 |
| `tls` | TCP + TLS 1.3, 유지 | 위와 같음 |
| `quic` | QUIC, 유지 | 요청마다 stream 하나 |
| `per-request` | TCP, 요청마다 새로 | 연결을 맺고 요청 하나를 보낸 뒤 닫음 |
| `tls-per-request` | TCP + TLS 1.3, 요청마다 새로 | 매번 전체 handshake |
| `quic-per-request` | QUIC, 요청마다 새로 | 매번 전체 handshake |
| `quic-0rtt` | QUIC, 요청마다 새로 | session ticket이 있으면 요청을 0-RTT data로 보냄 |

- signer는 TCP, TLS, QUIC을 각각 다른 port에서 함께 받는다. signer는 시작할 때 자체 서명한 Ed25519 인증서를 만들고, coordinator는 인증서의 chain과 이름을 검증하지 않는다. handshake의 서명 검증은 그대로 수행되므로 handshake의 왕복 수와 계산은 검증할 때와 같다. signer와 coordinator의 상호 인증은 구현하지 않았다.
- QUIC transport의 구현체는 quic-go v0.65.0이다. QUIC에서는 요청과 응답이 각자의 길이를 담고 있어 양쪽 모두 stream의 끝(FIN)을 기다리지 않는다. FIN이 손실돼도 요청이 늦어지지 않게 하기 위해서다.
- signer는 QUIC 연결을 handshake가 끝나기 전에 받아들인다. 0-RTT로 온 요청에 handshake 완료를 기다리지 않고 응답하기 위해서다.
- 0-RTT data는 재전송 공격(replay)에 노출된다. FROST 서명 요청은 한 번 쓴 nonce를 signer가 지우므로 다시 보내면 실패하고, threshold RSA의 share는 같은 입력에 같은 값이 나온다. 그 밖의 안전성은 검토하지 않았다.
- 기록에 요청별로 0-RTT 사용 여부를 남긴다. 0-RTT가 조용히 전체 handshake로 바뀌어도 서명은 성공하므로, 측정값이 어느 경우의 것인지 구분하기 위해서다.
- 단위 시험에 두 가지를 넣었다. 시작할 때 맺은 QUIC 연결이 요청이 끝난 뒤에도 그대로 쓰이는지, ticket을 받은 뒤의 요청이 실제로 0-RTT로 가는지다.

### 측정 전의 예측

측정 조건은 [results-phase3-connections.md](results-phase3-connections.md)와 같다. signer의 RTT_s는 번호 순서대로 50, 5, 100, 1, 20 ms이고, 지연은 signer의 송신 방향에 건다.

유지되는 QUIC 연결 하나에 관한 예측은 다음과 같다.

1. handshake 100개가 동시에 시작되고 loss가 2%일 때, T_hs 중앙값은 loss가 없을 때와 같다. TCP 연결 16개 이상에서 본 결과와 같은 이유다.
2. nudge가 없어도 꼬리가 약 200 ms가 아니라 probe timeout 수준이다. quic-go의 probe timeout은 "smoothed RTT + max(RTT 편차의 4배, 1 ms) + 상대의 max_ack_delay(25 ms)"다. 손실이 난 signer의 RTT_s를 r이라 하면 그 요청의 응답 시간은 약 2r + 26 ms다. 요청을 하나씩 보낼 때 가장 가까운 signer 셋(1, 5, 20 ms)에서는 28, 36, 66 ms가 된다.
3. 따라서 요청을 하나씩 보내고 loss가 2%일 때 D_sign의 p95는 (2, 5)에서 30~40 ms로 TCP + nudge(12.7 ms)보다 길고, (3, 5)에서도 30~40 ms로 TCP + nudge(43.9 ms)보다 조금 짧다. p99는 (3, 5)에서 약 66 ms다.
4. QUIC에 nudge를 더하면 (2, 5)에서는 TCP + nudge와 같은 수준(p95 20 ms 이하)으로 줄어든다. (3, 5)에서는 달라지지 않는다. nudge의 지연 시간(32 ms)이 가까운 signer의 probe timeout보다 늦기 때문이다.
5. loss가 없고 handshake가 몰릴 때는 QUIC이 TCP보다 느릴 수 있다. signer는 CPU 하나에서 QUIC packet을 사용자 영역에서 처리한다. 크기는 예측하지 않는다.

요청마다 연결을 새로 맺는 transport에 관한 예측은 다음과 같다.

6. loss가 없을 때 D_sign의 네트워크 항은 "서명 round 수 × 요청 하나의 왕복 수 × 선택된 signer 중 최대 RTT_s"이고, 요청 하나의 왕복 수는 TCP 2, TLS를 쓰는 TCP 3, QUIC 2, 0-RTT QUIC 1이다. precomputation을 쓰는 FROST (3, 5)에서는 40, 60, 40, 20 ms이고, 0-RTT QUIC은 유지되는 연결과 같다.
7. loss가 2%이면 연결 수립 packet의 손실이 꼬리를 만든다. TCP는 SYN의 재전송을 약 1초 기다리고, quic-go는 RTT 표본이 없을 때의 probe timeout인 200 ms를 기다린다. 0-RTT QUIC의 꼬리는 예측하지 않는다.

### signer 연결의 TLS와 QUIC 측정

| 측정 | 결과 directory | 내용 |
| --- | --- | --- |
| 유지되는 연결, 요청을 하나씩 | `results/2026-11-17-signer-quic-policy` | TCP·TLS·QUIC, nudge 유무, loss {0, 2}%, 조건마다 1,000회 |
| 유지되는 연결, 동시 handshake | `results/2026-11-17-signer-quic-conc` | TCP × {1, 64}, QUIC × {1, 4, 16}, 0-RTT QUIC, nudge 유무, loss {0, 2}%, 조건마다 5,000개 |
| QUIC 연결의 qlog | `results/2026-11-17-signer-quic-qlog` | QUIC × {1, 16}, loss {0, 2}%, coordinator 쪽 qlog 기록 |
| 요청마다 새 연결 | `results/2026-11-17-signer-setup` | scheme 3종, transport 5종, loss {0, 2}%, 조건마다 1,000회 |

결과는 [results-phase3-signer-transport.md](results-phase3-signer-transport.md)에 정리했다. 측정은 11월 17일 밤에 시작해 18일 새벽에 끝났다.

- 예측 7개 중 1번이 틀렸다. QUIC 연결 하나로는 handshake가 몰리고 loss가 있을 때 T_hs 중앙값이 돌아오지 않았다((3, 5)에서 11.0~14.4 ms 길었다).
- 원인을 추측으로 남기지 않으려고 coordinator가 signer와 맺는 QUIC 연결의 qlog를 기록하는 option을 넣고 다시 측정했다. loss 2%에서 congestion window가 63.5 packet에서 11.9 packet으로 줄었고, 한 번에 몰린 요청의 절반이 처음 전송되기까지 14.1 ms 이상을 기다렸다. 연결 16개에서는 이 대기가 없었다. 집계 스크립트는 `harness/analyze_signer_qlog.py`다.
- 이 결과로 11월 14일의 해석을 고쳤다. 연결 하나에 몰린 요청이 느려지는 원인을 손실 하나를 함께 기다리는 것으로만 적었는데, 줄어든 congestion window도 원인이다. [results-phase3-connections.md](results-phase3-connections.md)의 머리와 5절에 수정을 적었다. TCP에서 두 원인의 몫은 나누지 못했다.
- QUIC 연결을 signer당 4개 두면 nudge 없이 중앙값이 돌아왔고 꼬리가 TCP × 64 + nudge보다 짧았다. coordinator와 signer 사이 transport의 기본 구성을 "signer당 유지되는 QUIC 연결 4개"로 바꿨다.
- 뒤따르는 packet이 없는 연결에서 손실이 난 요청의 응답 시간은 QUIC에서 2r + 27 ms, TCP에서 2r + 206~213 ms였다(r은 signer의 RTT_s). QUIC의 값은 probe timeout의 식과 quic-go가 알리는 max_ack_delay 26 ms로 설명된다. TCP의 약 200 ms는 Linux의 재전송 timer 하한과 맞는다. 지금까지 "약 200 ms의 timer"라고만 적었던 값이다.
- 요청마다 연결을 맺는 TCP는 loss 2%에서 서명의 6.8~22.2%가 1초 넘게 늦었다. TLS를 쓰는 TCP의 세 조건에서 서명이 하나씩 실패했고, 모두 측정 도구의 연결 제한 시간 5초를 넘긴 경우다.
- 서명 하나가 실패하면 benchmark가 그 자리에서 끝나던 것을, 실패를 기록하고 계속하도록 바꿨다. 종료 상태는 여전히 실패를 알린다. 드문 실패 하나가 조건 전체의 측정을 버리게 하지 않기 위해서다.
- qlog 파일의 마지막 기록은 container를 지울 때 잘린다. 집계 스크립트는 잘린 기록을 건너뛴다. qlog 원본은 저장소에 넣지 않았다.

## 2026-11-18

### 남은 결과를 모두 내기 위한 계획

이 연구는 빅데이터처리 과목의 term project다. 과목이 안내한 프로젝트의 흐름은 "문제, 필요한 것, 설계, 실험, 결과"이고, 실험은 주장마다 하나씩 두며 data 크기와 worker 수를 바꿔 보라고 했다. 발표는 14주차에 5분 발표와 5분 질의응답으로 진행된다. [experiment-design.md](experiment-design.md)의 Phase 0~3에서 남은 항목을 아래 순서로 끝낸다.

| 묶음 | 내용 | 필요한 구현 |
| --- | --- | --- |
| 측정 batch 1 | Phase 1의 남은 조건(RTT_c 1·100 ms, client 링크 loss, 서버별 handshake 처리율), 실제 signer의 네트워크·시스템 격자, QUIC 연결 수, signer 장애와 RTT_s 변화, signer 처리율, jitter | 실행 스크립트의 option 추가 |
| 구현 | coordinator를 daemon으로 분리해 aioquic·quiche 서버에 실제 signer를 붙인다. s2n-quic 서버와 두 번째 client를 만든다. threshold ECDSA를 붙인다. | 세 갈래로 나누어 동시에 진행 |
| 측정 batch 2 | 구현체별 실제 signer handshake, s2n-quic과 두 번째 client, threshold ECDSA | 위 구현 |
| Phase 3 | 모델 적합, 예측, 세 region 배치의 전수 측정, Pareto frontier, 가설 H4 판정 | 집계 스크립트 |

- 실행 스크립트에 넣은 것: 정해진 rate로 handshake를 시작하는 부하(완료 여부와 무관하게 시작한다), client 링크의 loss, signer당 CPU 수, signer 링크의 jitter, client가 도는 동안 signer를 죽이거나 멈추거나 RTT_s를 바꾸는 event.
- signer에 CPU를 배정하는 순서에서 CPU 9번을 뺐다. 실험과 무관한 프로세스가 이 CPU를 계속 쓰고 있다. signer 5개 이하의 배정은 이전과 같다. CPU 9번은 signer 2가 쓰는 CPU 1번과 같은 물리 core의 thread여서, 지금까지의 측정에서 signer 2의 계산 시간에는 이 영향이 들어 있다.
- host의 logical CPU i와 i+8은 같은 물리 core다. front server에 "CPU 4개"를 주는 조건은 물리 core 2개의 thread 4개(4, 5, 12, 13번)다. 물리 core 4개를 따로 줄 여유가 host에 없다.
- batch 1은 측정 중에 code가 바뀌지 않도록 commit `afc2b0b`를 따로 checkout한 directory에서 실행한다. 명령은 `harness/campaigns/batch1.sh`에 있다.
- signer 복제본을 두는 조건은 측정하지 않기로 했다. FROST signer는 nonce 상태를 process 안에 두므로 복제본 사이에 commitment를 공유하는 설계가 따로 필요하다.

### batch 1의 예측

batch를 시작한 직후, 결과를 보기 전에 적었다. signer 배치는 번호 순서대로 RTT_s 50, 5, 100, 1, 20 ms다.

1. QUIC 연결 수. 손실로 줄어든 congestion window가 loss 2%에서 약 12 packet이었고 요청 100개가 packet 약 52개에 실렸다. 연결 하나에 실리는 packet 수가 window 이하가 되는 연결 수는 동시 handshake 30개에서 2개, 100개에서 4~5개, 300개에서 13개쯤이다. 따라서 30개에서는 연결 2개로 중앙값이 돌아오고, 300개에서는 연결 8개로도 돌아오지 않는다. window가 loss율의 제곱근에 반비례한다고 보면 동시 100개에서 loss 1%는 연결 3개, loss 5%는 연결 8개가 필요하다. RTT_s가 모두 50 ms이면 연결 하나일 때의 증가량이 20 ms일 때의 2배를 넘는다.
2. front server의 CPU. 동시 handshake 100개에서 T_hs에서 D_sign과 RTT_c를 뺀 값은 CPU가 1개일 때 2개일 때의 약 2배다. 수치 예측은 하지 않는다.
3. signer 처리율. threshold RSA의 share 계산은 약 4 ms이므로 CPU 1개인 signer는 초당 약 250개에서 포화한다. 그보다 높은 rate에서는 T_hs가 시간에 따라 계속 늘어난다. CPU 2개이면 포화점이 약 2배가 된다. FROST signer는 측정 범위(초당 1,600개)에서 포화하지 않는다.
4. signer를 죽일 때. TCP에서는 연결이 끊긴 것을 바로 알므로 fixed와 fastest의 handshake가 곧바로 실패한다. fastest는 RTT 추정을 표본이 있을 때만 고치므로 죽은 signer를 계속 고르고, 이후의 handshake가 모두 실패한다. hedge와 first는 남은 signer로 계속 성공하며 D_sign은 다음으로 가까운 signer의 RTT_s(50 ms) 수준이 된다. QUIC에서는 죽은 signer가 아무 신호도 보내지 않으므로 fixed와 fastest의 handshake가 서명 제한 시간 15초를 기다린 뒤 실패한다.
5. signer를 멈출 때(응답만 없음). 두 transport 모두 4번의 QUIC 경우와 같다.
6. signer의 RTT_s가 5 ms에서 200 ms로 바뀔 때. fastest는 RTT 추정의 시간 창 두 개(각 5초)가 지나면 그 signer를 빼므로 5~10초 동안 T_hs가 약 200 ms 늘었다가 돌아온다. fixed는 끝까지 늘어난 채로 남는다. hedge는 영향을 받지 않는다.
7. 네트워크 격자. handshake를 하나씩 할 때 T_hs 중앙값에서 D_sign 중앙값을 뺀 값은 RTT_c마다 일정하며 RTT_c에 약 1 ms를 더한 값이다.
8. client 링크의 loss. quic-go 서버는 서명이 끝날 때까지 응답하지 않으므로 client의 RTT 표본에 D_sign이 들어간다. 손실된 handshake packet의 복구가 그만큼 늦어져, T_hs의 p95는 D_sign이 클수록 "loss가 없을 때의 값 + D_sign"보다 더 많이 늘어난다. 서명을 기다리는 동안 먼저 응답하는 quiche에서는 이 증가가 작다.
9. 서버별 handshake 처리율(서명 지연 주입). aioquic은 "1 / (서명 지연 + handshake 처리 시간)"에서 포화한다. 지연 5 ms에서 초당 약 110개, 20 ms에서 약 45개다. quic-go와 quiche의 포화점은 서명 지연과 무관하다.
10. RTT_c 1 ms와 100 ms. 단일 handshake의 T_hs는 20 ms일 때와 같이 주입 지연만큼만 늘어난다.
11. jitter. D_sign 중앙값은 변하지 않고 p95가 가장 먼 signer의 jitter 표준편차의 2배쯤 늘어난다.

### Phase 3의 모델과 예측

지금까지의 측정으로 서명 지연 모델을 만들어 `harness/model.py`에 넣었다. 대상은 연결을 유지하고 서명을 하나씩 만드는 조건이다.

- signer 하나의 응답 시간은 그 signer의 RTT_s(r)에 계산 시간을 더한 값이다. 요청이나 응답이 손실되면 응답 시간이 2r + c가 되며 c는 TCP 210 ms, QUIC 27 ms다. nudge를 쓰면 ping을 보낸 시각에 2r을 더한 값이 된다. QUIC에서는 ping이 probe timeout보다 먼저 나갈 때만 그렇다.
- 정책별 결합은 구현을 따른다. fixed와 fastest는 고른 t개 중 가장 늦은 응답, first는 n개 중 t번째 응답이다. hedge는 round마다 가장 늦은 signer의 합이 가장 작은 t개 부분집합의 값이다. 지연 hedge는 가까운 t개가 지연 시간 안에 끝나지 않으면 그 시점부터 넓힌 집합의 t번째 응답을 더한다.
- 예산을 둔 hedge, round가 2개인 FROST의 지연 hedge, 요청마다 맺는 연결, handshake가 몰리는 조건은 모델에 넣지 않았다.
- 기존 측정 170개 조건과 비교한 절대 오차의 중앙값은 D_sign의 중앙값에서 0.05 ms, p95에서 0.75 ms, p99에서 2.80 ms였다. 오차의 90번째 백분위는 각각 0.63, 7.37, 18.53 ms다. p99의 오차는 TCP에서 loss가 있을 때 크다. 모델은 재전송된 packet이 다시 손실되는 경우를 넣지 않았다.

가설 H4의 실험은 signer 5개를 RTT_s가 5, 20, 80 ms인 세 region에 나누어 두는 배치 5가지와 t = 2, 3, 4에서, 모델이 다루는 정책을 모두 측정하는 것이다. 예측은 측정 전에 `results/2026-11-18-phase3/predictions-*.csv`에 적었다. 예측의 요지는 다음과 같다.

1. loss가 없을 때 D_sign 중앙값은 정책과 무관하게 "t번째로 가까운 signer의 RTT_s + 0.34 ms"다. 배치 5/5/20/20/80에서 t = 2, 3, 4일 때 5.3, 20.3, 20.3 ms다.
2. loss 2%에서 p99가 가장 낮은 정책은 여유 signer가 가까이 있는지에 따라 갈린다. t번째와 t+1번째로 가까운 signer가 같은 region에 있으면 hedge가 가장 낮고 p99가 중앙값과 같다(5/5/20/20/80의 t = 3에서 20.3 ms). 여유 signer가 먼 region에 있으면 지연 hedge나 nudge가 가장 낮다(5/5/20/20/80의 t = 2에서 지연 hedge 15.0 ms).
3. fastest만 쓰면 QUIC에서 p99가 "2r + 27 ms"가 된다. t번째 signer가 5, 20, 80 ms일 때 37.3, 67.3, 187.3 ms다.
4. 가설 H4의 판정 기준은 배치와 t마다 모델이 p99로 고른 상위 3개 정책 안에 실측 p99가 가장 낮은 정책이 들어가는지다. 모델에서 p99가 같은 정책은 같은 순위로 센다.

region 하나가 통째로 침해되어도 key가 유출되지 않으려면 한 region의 signer 수가 t보다 적어야 한다. signer 5개를 세 region에 두면 t = 2에서는 이 조건을 만족하는 배치가 없고, t = 3에서는 region별 signer 수가 2, 2, 1인 배치만 만족한다. 이때 D_sign의 하한은 두 번째로 가까운 region의 RTT_s다. 이 조건을 넣은 구성 비교는 측정 뒤에 정리한다.

### 구현체별 실제 signer를 위한 구현

- coordinator를 `qth-coordd`라는 daemon으로도 쓸 수 있게 했다. 서버는 TLS가 인증서 key에 서명을 요청하는 message를 daemon에 그대로 보내고 서명을 받는다. protocol은 `harness/internal/coordd`에 있다. daemon은 서버와 같은 container에서 돌며, `serve-with-coordd`가 daemon이 준비된 뒤에 서버를 시작한다.
- 이 방식으로 aioquic, quiche, s2n-quic 서버에 실제 signer를 붙였다. aioquic은 서명 호출 안에서 daemon의 응답을 기다린다. quiche는 BoringSSL의 비동기 private key method로 요청만 보내고 event loop로 돌아간다.
- quic-go 서버는 coordinator를 process 안에 두는 기존 방식과 daemon을 거치는 방식을 모두 지원한다. 두 방식을 같은 조건에서 재면 daemon을 거치는 비용이 나온다.
- s2n-quic 서버는 인증서 key의 종류에 따라 TLS library가 다르다. RSA와 P-256 key에서는 s2n-tls의 비동기 private key 연산을 쓴다. 이 연산은 서명할 message의 SHA-256 digest만 넘겨주므로, daemon에 digest를 받는 listener를 하나 더 두었다. Ed25519 key는 s2n-tls가 지원하지 않아 rustls를 쓰고, handshake마다 TLS 처리를 별도 thread에 맡긴다. s2n-tls 경로는 key exchange로 X25519를, 나머지 서버와 rustls 경로는 X25519MLKEM768을 협상한다. 이 차이는 맞추지 못했다.
- rustls 경로에서 s2n-quic 서버의 handshake가 client 왕복 한 번만큼 늦었다(loopback에 지연 20 ms를 건 시험에서 57.7 ms, 고친 뒤 27.3 ms). 원인은 s2n-quic 1.92.0의 TLS offload에서 TLS thread가 Handshake flight를 넘길 때 QUIC 쪽을 깨우지 않는 것이었다. 서버가 client의 다음 packet을 받은 뒤에야 flight를 보냈다. offload된 session을 한 번 더 poll하게 감싸서 우회했다.
- 두 번째 client는 s2n-quic(rustls)으로 만들었다. quic-go client와 같은 option과 기록 형식을 쓴다. ECDSA P-256, Ed25519, RSA 인증서를 모두 검증하는 것을 실행으로 확인했다. 정해진 rate의 부하와 qlog는 quic-go client만 지원한다.
- 두 번째 client의 처음 구현은 quic-go 서버와 handshake할 때 왕복이 한 번 더 들었다(서명 지연 50 ms에서 109.1 ms, quic-go client는 81.7 ms). client가 X25519 key share만 보내 서버가 HelloRetryRequest로 되물었기 때문이다. quic-go client와 같은 순서의 group과 key share(X25519MLKEM768, X25519)를 보내고 Initial datagram 크기를 1,280 byte로 맞춘 뒤에는 83.4 ms였다. key exchange group을 구현체마다 같게 맞추는 통제가 실제로 결과를 바꾸는 예다.
- 구현 세 갈래(daemon과 서버 연동, s2n-quic, threshold ECDSA)는 서로 다른 branch에서 동시에 진행하고 loopback에서 확인한 뒤 합쳤다.

### threshold ECDSA 구현

- library는 coinbase/kryptology v1.8.0의 GG20(Gennaro와 Goldfeder의 2020년 threshold ECDSA protocol)을 골랐다. 조건은 P-256 지원, t-of-n 서명, cgo 없이 build되는 것이었다.
- 검토한 다른 library와 제외한 이유는 다음과 같다. bnb-chain/tss-lib은 secp256k1과 Ed25519만 등록돼 있고 서명 round가 9개다. taurusgroup/multi-party-sig는 secp256k1만 지원한다. getamis/alice는 P-256 시험이 없고 message가 signer 생성 시점에 고정된다. bronlabs/bron-crypto(DKLs23)는 기본 build에 OpenSSL header가 필요하고, v0.1.0에서는 부분 서명을 직렬화할 수 없어 coordinator가 결합하지 못했다. coinbase/cb-mpc는 C++ library다.
- kryptology는 2022년 9월 이후 갱신이 없는 archive 상태다. library의 보안 문서는 Alpha-Rays 공격에 대한 수정이 들어갔다고 적고 있으며, GG20 자체는 저자들이 쓰지 말라고 밝힌 protocol이다. 이 구현은 지연 측정용이며 실제 배치의 근거로 쓰지 않는다.
- key는 dealer가 나눈다. dealer가 signer마다 2,048 bit Paillier key와 range proof parameter를 만든다. (3, 5) key를 만드는 데 약 29초가 걸렸다(개발 장비 기준).
- signer끼리는 직접 통신하지 않는다. coordinator가 protocol message를 모아서 다시 나누어 주므로 protocol의 round 하나가 coordinator와 signer 사이 요청 round 하나가 된다. precomputation이 없으면 서명 하나에 요청 round가 6개이고, message는 여섯 번째 round에만 들어간다.
- precomputation을 켜면 앞의 5개 round를 미리 끝낸 presignature를 signer 집합마다 256개 보관하고, 서명할 때는 round 하나만 쓴다. 남은 수가 절반 이하가 되면 8개씩 다시 채운다. 측정 조건 하나의 서명 수를 128개 미만으로 두어 다시 채우는 계산이 측정 구간에 들어오지 않게 했다.
- kryptology가 마지막 round의 검증 부분을 따로 내보내지 않아, precomputation을 켜도 signer가 서명마다 다른 signer의 proof를 검증한다. 개발 장비의 loopback에서 서명 하나의 signer 계산 시간은 precomputation이 없을 때 (2, 3) 198 ms, (3, 5) 360 ms였고, 켰을 때 13 ms, 26 ms였다.
- 정책은 fixed와 fastest만 지원한다. first와 hedge는 이 scheme을 거부한다.
- `internal/tsig`의 단위 시험은 ECDSA key 생성 때문에 3~4분이 걸린다.

### 측정 대기열

측정은 host의 tmux session에서 순서대로 실행한다. 각 묶음은 code가 바뀌지 않도록 별도로 checkout한 directory에서 돈다.

| 순서 | 내용 | script | commit |
| --- | --- | --- | --- |
| 1 | batch 1 | `harness/campaigns/batch1.sh` | `afc2b0b` |
| 2 | 구현체별 실제 signer, client 링크 loss | `harness/campaigns/batch2.sh`의 `impl-signer`, `impl-client-loss` | `bd67b9f` |
| 3 | threshold ECDSA | `harness/campaigns/batch3.sh` | `bd67b9f` |
| 4 | 세 region 배치의 전수 측정 | `harness/campaigns/phase3.sh` | `bd67b9f` |
| 5 | s2n-quic 서버의 지연 주입 측정, 두 번째 client | `harness/campaigns/batch2.sh`의 `p1-s2n`, `client-s2n` | `bd67b9f` |

### batch 1의 결과

batch 1은 조건 422개를 4시간 9분 동안 측정했고 실패한 실행은 없었다. 결과는 문서 세 개로 나누어 적었다.

- [results-phase1-grid.md](results-phase1-grid.md): 서명 지연을 주입한 측정의 나머지 변인이다. RTT_c 1 ms와 100 ms에서도 T_hs는 주입한 지연만큼만 늘었다. client link의 loss에서는 서버가 client의 Initial을 먼저 확인해 주는지가 꼬리를 정했다. quic-go 서버는 ServerHello가 손실되면 client가 RTT 표본 없이 200 ms를 기다렸다. aioquic의 처리율은 "1 / (지연 + 1.6 ms)"였다.
- [results-phase2-environment.md](results-phase2-environment.md): 실제 signer를 붙인 handshake의 네트워크와 시스템 변인이다. T_hs 중앙값은 72개 조건 모두에서 "RTT_c + D_sign + 약 1 ms"였다. handshake가 몰리면 signer의 계산이 T_hs를 정했다. threshold RSA signer는 CPU 하나로 초당 약 200개를 처리했다.
- [results-phase3-robustness.md](results-phase3-robustness.md): QUIC 연결 4개는 동시 handshake 300개, loss 5%, RTT_s 50 ms까지 충분했다. signer 하나가 응답하지 않으면 fixed와 fastest는 모든 handshake가 실패했고 hedge와 first는 전부 성공했다. hedge 예산 0.2는 장애 때 성공률을 21%로 떨어뜨렸다.

예측과 달랐던 것과 그 밖의 기록은 다음과 같다.

- 예측 1(필요한 QUIC 연결 수)은 2~3배 많았다. 요청이 한 순간에 몰린다고 가정한 것이 원인으로 보인다.
- 예측 3 가운데 "signer의 CPU가 2개이면 포화점이 2배"는 틀렸다. signer들에게 준 logical CPU가 물리 core를 나누어 쓰고 있었다. 물리 core를 나눈 조건은 host에 여유가 없어 측정하지 못했다.
- 예측 4 가운데 "TCP에서는 곧바로 실패한다"는 틀렸다. 끊긴 것은 바로 알지만 다시 연결하는 시도가 제한 시간 5초를 채운다.
- 예측 6 가운데 "hedge는 영향을 받지 않는다"는 틀렸다. 세 번째로 가까운 signer가 바뀌므로 T_hs가 30 ms 늘었다.
- 예측 8은 조건당 400회로는 판정할 수 없었다. 대신 늦어진 handshake의 client qlog를 열어, 꼬리의 원인이 서명 지연이 아니라 client가 RTT 표본을 얻는 시점이라는 것을 확인했다.
- 예측 9는 식이 맞고 handshake 처리 시간의 값이 틀렸다(4 ms로 잡았고 실제는 약 1.6 ms).
- rsync로 host의 `results/`를 통째로 가져오면 예전 측정의 압축하지 않은 `*.jsonl`이 함께 온다. 압축한 파일이 이미 있는 것은 지우고 commit했다.
- threshold signing을 QUIC handshake와 함께 측정한 선행 연구를 web search로 다시 찾았다. 검색어는 "threshold signature TLS 1.3 handshake latency QUIC"과 "threshold signing QUIC handshake latency evaluation FROST threshold ECDSA threshold RSA"다. 나온 것은 post-quantum 서명, 인증서 크기, Keyless SSL에 관한 연구였고 해당하는 연구는 찾지 못했다.

### 남은 측정의 예측

구현체별 실제 signer 측정(대기열 2번)은 예측을 적지 않고 시작했다. 그 결과를 본 뒤, 아직 결과를 보지 않은 대기열 3번과 5번의 예측을 적는다. 3번은 이 시점에 실행 중이고 5번은 시작 전이다.

threshold ECDSA(대기열 3번)의 예측이다. 개발 장비의 loopback에서 잰 signer 계산 시간(precomputation이 있을 때 (2, 3) 13 ms, (3, 5) 26 ms, 없을 때 198 ms, 360 ms)을 근거로 삼는다.

1. precomputation이 없으면 요청 round가 6개이므로 D_sign 중앙값은 "6 × RTT_s + 계산 시간"이다. RTT_s 50 ms와 5 ms의 차이는 270 ms다.
2. precomputation이 있으면 "RTT_s + 계산 시간"이다. 계산 시간은 (2, 3)에서 15~20 ms, (3, 5)에서 30~40 ms다. RTT_s 50 ms와 5 ms의 차이는 45 ms다.
3. handshake를 하나씩 할 때 T_hs − D_sign은 다섯 서버 모두 20.6~21.8 ms다. 다른 scheme과 같다.
4. 동시 handshake 10개에서는 signer의 계산이 T_hs를 정한다. signer가 요청 하나에 13 ms를 쓰면 한 wave가 130 ms 이상 걸린다. 서명을 차례로 처리하는 aioquic과 나머지 서버의 T_hs 중앙값 차이는 1.5배 안이다. FROST에서는 이 차이가 2배였다.
5. signer link의 loss 2%에서, precomputation이 없으면 요청과 응답이 24번 오가므로 서명의 38% 이상이 손실을 겪는다. ECDSA의 message는 packet 여러 개에 실리므로 실제 비율은 더 높다. p95는 QUIC에서 37 ms 이상, TCP에서 210 ms 이상 늘어난다. precomputation이 있으면 8% 안팎이 손실을 겪고 p95가 같은 크기만큼 늘어난다.

s2n-quic 서버의 지연 주입 측정과 두 번째 client(대기열 5번)의 예측이다. 4번과 6번은 구현체별 실제 signer 측정에서 s2n-quic 서버가 손실된 handshake packet을 700~1,000 ms 뒤에야 다시 보낸 것을 본 뒤에 적었다.

1. s2n-quic 서버에서 handshake를 하나씩 하면 T_hs는 RTT_c 1, 20, 100 ms 모두에서 주입한 지연만큼만 늘어난다(차이 1 ms 미만).
2. 동시 handshake 100개, 지연 50 ms에서 T_hs 중앙값은 quiche와 비슷한 75~85 ms다. 서명을 기다리는 handshake가 서로를 막지 않는다.
3. 정해진 rate에서는 초당 1,600개까지 지연 0, 5, 20 ms 모두 포화하지 않는다.
4. client link의 loss 2%에서는 TLS library와 무관하게 handshake의 3~5%가 700~1,000 ms 늦어진다. 다시 보내는 시점을 정하는 것은 s2n-quic의 손실 복구다.
5. 두 번째 client(s2n-quic)로 재도 T_hs는 네 서버 모두에서 주입한 지연만큼만 늘어난다(차이 1.5 ms 미만).
6. 두 번째 client는 RTT 표본이 없을 때의 probe timeout이 약 1초다(quic-go client는 200 ms). client link의 loss 2%에서 quic-go 서버는 ServerHello가 손실되면 약 1초 늦어지고, 먼저 확인해 주는 quiche는 RTT_c의 3배쯤만 늦어진다. 두 서버의 p99 차이가 quic-go client일 때(약 140 ms)보다 커진다.
7. 두 번째 client로 실제 signer를 붙인 handshake를 재면 T_hs − D_sign은 20~22 ms다.

### 구현체별 실제 signer와 threshold ECDSA의 결과

대기열 2번과 3번이 끝났다. 2번은 조건 170개가 모두 정상 종료했고, 3번은 조건 43개 중 하나(precomputation을 쓴 ECDSA (3, 5), RTT_s 0 ms)가 서명 하나의 실패로 비정상 종료했다. 결과는 [results-phase2-implementations.md](results-phase2-implementations.md)와 [results-phase2-ecdsa.md](results-phase2-ecdsa.md)에 적었다.

- 실제 signer를 붙인 handshake에서 T_hs − D_sign은 다섯 서버가 20.6~21.8 ms였다. coordinator를 daemon으로 빼는 비용은 동시 100개에서 0.4~3.3 ms였다.
- client link의 loss 2%에서 s2n-quic 서버는 handshake의 3.5~5%가 150 ms 넘게 늦어졌고 대부분이 360~1,020 ms였다. client qlog를 열어 보니 서버가 손실된 handshake packet을 약 1초 뒤에 다시 보냈다. s2n-quic의 RTT 초깃값이 333 ms여서 표본이 없을 때의 probe timeout이 999 ms다. 서버가 처음 보내는 packet이 ACK만 담고 있어 서버는 표본을 얻지 못한다.
- threshold RSA의 share 계산 시간이 실행마다 4.9 ms 또는 6.8 ms로 갈리는 것을 처음에는 설명하지 못했다. ECDSA 결과를 signer 집합별로 나누어 보고 원인을 찾았다. signer 5의 CPU(8번)는 signer 1의 CPU(0번)와 같은 물리 core의 thread다. 둘이 함께 뽑힌 실행에서만 계산이 1.4배 걸렸다. host에서 signer에 줄 수 있는 물리 core가 4개여서 signer 5개를 모두 떼어 놓을 수 없다. RTT_s가 같은 조건에서 fastest가 어느 집합을 고르는지는 실행마다 다르므로, 이런 조건의 threshold RSA와 ECDSA 값은 실행끼리 2 ms(RSA), 15 ms(ECDSA)까지 다를 수 있다.
- threshold ECDSA는 precomputation을 쓰면 D_sign이 "RTT_s + 14~36 ms"였다. presignature 하나를 만드는 데 0.23~0.45초가 들어 서명 rate가 signer의 CPU 하나당 초당 2~4개로 묶인다.
- ECDSA의 presignature는 signer 집합에 묶인다. coordinator는 시작할 때 fastest가 고른 집합 하나만 채운다. RTT_s가 같은 조건에서 fastest의 집합이 측정 중에 바뀌자 새 집합의 pool을 채우는 계산이 서명과 겹쳤고, (3, 5)에서는 handshake가 100개 중 1~6개 실패했다. 측정 설계의 실수(집합을 고정하지 않음)이면서, 이 scheme에서 집합 전환이 비싸다는 결과이기도 하다. 해당 조건 10개는 지연의 크기를 말할 때 빼고 따로 적었다.
- 2번은 예측을 적지 않고 시작했다. 3번과 5번의 예측은 2번의 결과를 본 뒤, 3번과 5번의 결과를 보기 전에 적었다. 3번의 예측 5개는 네트워크 항과 계산 시간의 크기에서 맞았다. 집합 전환의 비용은 예측하지 못했다.

### Phase 3 측정과 검증 측정의 예측

대기열 4번(세 region 배치의 전수 측정, 조건 264개)이 끝났고 실패한 실행은 없었다. 이 측정은 coordinator만 실행해 D_sign을 잰 것이다. 결과에서 고른 구성이 실제 handshake에서도 같은 순서와 크기를 보이는지 `harness/campaigns/phase3-verify.sh`로 확인한다. 조건은 quic-go 서버, RTT_c 20 ms, signer link의 loss 2%, 조건마다 handshake 1,000회다. 실행하기 전에 예측을 적는다.

1. T_hs 중앙값은 D_sign 측정의 중앙값에 21.0 ms를 더한 값과 0.5 ms 안에서 같다. 배치 5/5/20/20/80에서 FROST t = 2의 fastest와 hedge4는 26.3, 26.5 ms, t = 3의 fastest, nudge, hedge4, 지연 hedge는 41.4, 41.4, 41.7, 41.5 ms, t = 4의 fastest와 nudge는 41.6 ms, threshold RSA t = 3의 fastest와 first + nudge는 45.2 ms다. 배치 5/5/5/20/80의 t = 2 hedge4는 26.6 ms, t = 3 hedge5는 27.6 ms다.
2. p99의 순서는 D_sign 측정과 같다. 같은 t에서 hedge4(또는 first)의 p99가 fastest보다 낮다.
3. hedge와 first의 T_hs p99는 D_sign의 p99에 21 ms를 더한 값과 5 ms 안에서 같다(t = 2 hedge4 41.6 ms, t = 3 hedge4 45.7 ms, threshold RSA first + nudge 47.4 ms, 배치 5/5/5/20/80의 30.5 ms와 43.0 ms). fastest와 nudge의 p99는 표본 10개로 정해지고 D_sign 측정에서도 76~103 ms로 흩어졌으므로 25 ms 안에서 같다고만 예측한다.

## 2026-11-19

### s2n-quic 서버와 두 번째 client의 결과

대기열 5번이 2026-11-18 15:11에 끝났다. 조건 108개가 모두 정상 종료했다. 결과는 [results-phase1-s2n.md](results-phase1-s2n.md)에 적었다.

- s2n-quic 서버에서도, 두 번째 client로 재도 T_hs는 주입한 지연만큼만 늘었다. 서버 4개와 client 2개의 조합에서 덧셈이 성립한다.
- s2n-quic 서버는 handshake가 몰릴 때 가장 안정적이었고(초당 1,600개에서 T_hs 변화 없음), client link의 loss에는 가장 약했다. ServerHello가 손실되면 client가 ClientHello를 다시 보내도 ACK만 돌려주고 자신의 timer를 기다렸다. s2n-tls 경로와 rustls 경로에서 같았으므로 TLS library가 아니라 s2n-quic의 동작이다.
- 예측 6(두 번째 client에서 quic-go 서버는 약 1초, quiche는 RTT_c의 3배쯤 늦어진다)은 틀렸다. quic-go 서버에서는 약 200 ms 늦어진 handshake가 나왔다. client의 timer가 1초이므로 서버가 스스로 다시 보낸 것으로 본다. quic-go client로 잰 측정에서는 client와 서버의 timer가 둘 다 200 ms여서 "client가 200 ms를 기다렸다"고만 읽었다. client 구현체를 바꾸어야 드러나는 것이었다.
- 두 번째 client에서는 네 서버 모두 handshake의 1~3.5%가 약 1초 늦어졌다. 이 client는 qlog를 남기지 않아 원인을 packet 단위로 보지 못했다. 두 번째 client를 만들 때 qlog를 빼기로 한 결정의 비용이다.
- 두 번째 client로 handshake 100개를 함께 시작하면 loss가 없어도 quiche 서버에서 49%, s2n-quic 서버에서 6%가 약 1.09초 걸렸다. client는 연결마다 socket을 따로 열므로 client의 수신 buffer가 넘친 것은 아니다. 서버 socket의 수신 buffer가 넘쳤을 가능성을 생각했으나 확인하지 않았다.
- 정해진 rate의 측정은 측정용 checkout의 집계 script가 예전 것이어서 rate 지표가 빠졌다. 압축한 기록으로 다시 집계했다. 이 묶음의 요약에는 qlog에서 뽑는 열이 비어 있다.

### Phase 3 결과의 정리

- 가설 H4의 판정 기준(모델의 상위 3개 순위)은 24개 묶음 모두에서 충족됐지만, 15개 묶음에서 상위 3개 순위가 정책 전부였다. 기준을 정할 때 "이 기준에서 틀릴 수 있는 경우가 몇 개인가"를 세지 않았다. 결과 문서에는 기준의 충족 여부와 함께 1순위의 일치 수(11개)와 regret을 적었다.
- 모델에서 지연 hedge의 넓히는 단계가 구현과 달랐다. 넓힐 때 시작하는 것은 처음의 집합이 아닌 부분집합들이므로 여유 signer의 응답이 반드시 필요하다. `harness/model.py`를 고쳤다. 측정 전에 commit한 예측 file은 그대로 두었고, 고친 모델의 값은 `results/2026-11-18-phase3/policies-model-vs-measured.csv`에 따로 넣었다.
- 결과 문서의 구성 비교표에 정책별 중앙값 대신 묶음의 최저 중앙값을 적은 곳이 있었다. hedge5는 share를 30개 계산해 중앙값이 1.2 ms 길다. 표를 그 정책의 값으로 고쳤다.

### 검증 측정의 결과

`harness/campaigns/phase3-verify.sh`를 14:07~14:36에 실행했다. 조건은 12개이고 handshake 12,000개가 전부 성공했다. 결과는 [results-phase3-optimization.md](results-phase3-optimization.md) 7절에 적었다.

- 예측 1(T_hs 중앙값이 D_sign 중앙값 + 21.0 ms와 0.5 ms 안에서 같다)은 12개 중 11개에서 맞았다. 하나는 0.58 ms였다.
- 예측 2(p99의 순서가 같다)는 hedge4와 first가 fastest보다 낮다는 점에서 맞았다. t = 3에서 fastest와 nudge의 순서가 바뀌었다.
- 예측 3 가운데 hedge와 first의 p99는 0.3~1.2 ms 차이로 맞았다. fastest는 6개 중 2개가 25 ms 밖이었다. FROST t = 2의 fastest는 D_sign p99가 앞선 측정에서 95.2 ms, 이번에 44.2 ms였다.
- 이 측정으로 같은 조건을 두 번 잰 값이 처음 생겼다. 손실 복구를 기다리는 정책의 p99는 표본 1,000개로는 수십 ms씩 흔들린다. Phase 3의 순위 비교(1순위 일치 11개, regret)는 조건마다 한 번 잰 p99로 한 것이므로 fastest와 nudge가 얽힌 부분은 그만큼 불확실하다. 결과 문서의 한계에 적었다.
- 검증 측정의 첫 두 조건이 도는 동안 같은 host에서 대기열 5번의 집계 script를 실행했다(약 1분). 측정 중인 host에서 다른 일을 하지 않는다는 규칙을 어겼다. 두 조건의 T_hs 중앙값은 예측과 0.2 ms 안에서 같았고 실행 전 1분 load average는 1.6이었다. 다시 측정하지는 않았다.

## 2026-11-23

### 논문화를 위한 반복 측정의 계획과 예측

결과를 논문으로 쓰기로 했다. 지금까지의 측정은 조건마다 한 번씩이어서 p99가 표본 10개 안팎으로 정해졌고, 같은 조건을 두 번 잰 12개에서 fastest의 p99가 51 ms까지 달랐다. 꼬리에 관한 주장에는 반복 측정이 필요하다. `harness/campaigns/repeat.sh`로 네 묶음을 측정한다. 결과는 `results/2026-11-23-repeat`에 둔다.

| 묶음 | 내용 | 반복 |
| --- | --- | --- |
| policies | 배치 5/5/20/20/80, signer link의 loss 2%에서 정책별 D_sign. FROST t = 2, 3, 4와 threshold RSA t = 3 | 서명 1,000개씩 5회 |
| newplace | RTT_s가 10, 40, 120 ms인 배치 10/10/40/40/120과 10/40/40/120/120에서 FROST의 정책 전부 | 서명 1,000개씩 2회 |
| handshake | 배치 5/5/20/20/80, t = 3의 fastest와 hedge4(FROST), fastest와 first + nudge(threshold RSA)를 실제 handshake로 | handshake 1,000개씩 5회 |
| clientloss | client link의 loss 2%, RTT_c 20 ms, 주입한 지연 0, 20, 100 ms, 서버 4개 | handshake 2,000개씩 1회 |

host는 11월 20일에 다시 시작됐고, CPU 9를 쓰던 process가 없다. 측정 전 1분 load average는 0.0이다. signer의 CPU 배정은 앞선 측정과 같게 둔다.

newplace는 "가장 낮은 p99는 여유 signer의 위치로 정해진다"는 규칙을 찾는 데 쓰지 않은 배치다. 이 규칙은 앞선 측정을 본 뒤에 찾았으므로 새 배치로 검증한다. 모델(지연 hedge를 고친 것)의 예측은 측정 전에 `results/2026-11-23-repeat/predictions-newplace.csv`에 적었다. 판정에 쓰는 p99는 두 회를 합친 서명 2,000개의 p99다.

측정 전의 예측과 판정 기준이다.

1. 규칙의 검증. 묶음(배치와 t) 6개에서 정책 가운데 가장 낮은 p99는 "min((t + 1)번째로 가까운 signer의 RTT_s, 2 × t번째 signer의 RTT_s + 27 ms)"보다 0~10 ms 길다. 규칙의 값은 배치 10/10/40/40/120의 t = 2, 3, 4에서 40, 40, 107 ms, 배치 10/40/40/120/120에서 40, 107, 120 ms다. 6개 가운데 5개 이상이 범위에 들면 규칙이 유지된다고 본다. 범위를 벗어날 수 있는 경우가 6개다.
2. 모델의 검증. 묶음마다 모델이 p99로 1순위에 둔 정책들(0.5 ms 안의 동률 포함) 가운데 실측 p99가 가장 낮은 것과 그 묶음의 실측 최저 p99의 차이가 5 ms 이하다. 6개 가운데 5개 이상이면 "모델이 최저 정책을 고른다"고 본다. 앞선 측정에서 쓴 "상위 3개 순위" 기준은 쓰지 않는다.
3. 모델은 t번째 signer의 RTT_s가 40 ms일 때 nudge(계수 1.5)가 fastest보다 나쁘다고 예측한다(p99 142 ms 대 107 ms). ping을 보내는 시점(62 ms)이 QUIC의 손실 복구(67 ms)보다 조금 앞서 timer를 다시 맞추기 때문이다. 해당하는 묶음 4개 가운데 3개 이상에서 nudge의 p99가 fastest보다 15 ms 넘게 길 것으로 예측한다. fastest의 p99가 흔들리므로 이 예측은 틀릴 가능성이 크다고 본다.
4. policies에서 hedge와 first의 p99는 5회의 최댓값과 최솟값의 차이가 5 ms 이하다. fastest의 p99는 그 차이가 20 ms 이상이다. 5회 평균으로 보면 t = 3에서 hedge4의 p99는 23~27 ms, fastest의 p99는 65~95 ms다.
5. handshake에서 T_hs p99의 5회 평균은 hedge4 44~48 ms, first + nudge 46~50 ms다. fastest는 FROST와 threshold RSA 모두 80~105 ms이고 5회의 차이가 20 ms 이상이다.
6. clientloss에서 중앙값보다 150 ms 넘게 늦어진 handshake의 비율은 quic-go 1~3.5%, s2n-quic 2~3.5%, quiche 0.3% 이하다. aioquic은 지연 0과 20 ms에서 0.3% 이하, 100 ms에서 1~3%다. s2n-quic에서 늦어진 handshake의 80% 이상이 900 ms를 넘는다.

### 반복 측정의 결과와 추가 측정

`repeat.sh`의 네 묶음이 11월 23일 21:57부터 24일 02:02까지 돌았다. 조건 194개가 모두 정상 종료했다. 측정 전 1분 load average는 0.0~4.8(중앙값 0.47)이었다. policies 묶음의 3~4회째에 다른 사용자의 부하가 있었다. 집계는 `harness/analyze_repeat.py`로 했다.

예측의 판정이다.

1. 규칙의 검증은 기준을 충족하지 못했다. 6개 묶음 가운데 4개만 범위(규칙의 값보다 0~10 ms 김)에 들었다. 배치 10/10/40/40/120의 t = 2에서는 nudge가 38.1 ms로 규칙의 값(40 ms)보다 낮았고, t = 4에서는 가장 낮은 p99가 122.2 ms로 규칙의 값(107 ms)보다 15.2 ms 길었다. t = 4에서는 가까운 signer 넷이 모두 응답해야 하므로 서명의 약 15%가 손실을 겪는다. 이때 p99는 손실 한 번의 복구 시간이 아니라 손실이 겹치는 경우로 정해진다.
2. 모델의 검증은 기준을 충족했다. 6개 묶음 모두에서 모델의 1순위 정책 가운데 가장 좋은 것이 실측 최저였다. 다만 1순위에 든 정책 수가 묶음마다 1, 2, 3, 3, 5, 1개였다. 정책 4개 중 3개, 6개 중 5개가 1순위인 두 묶음에서는 이 기준이 가르는 것이 거의 없다. 1순위 가운데 가장 나쁜 것은 실측 최저보다 최대 35.1 ms 길었다.
3. nudge의 예측은 맞았다. t번째 signer의 RTT_s가 40 ms인 묶음 4개 가운데 3개에서 nudge의 p99(144.4~144.7 ms)가 fastest보다 20.9~35.3 ms 길었다. 나머지 하나는 fastest의 p99가 두 회에서 121.4 ms와 154.3 ms로 갈려 차이가 1.1 ms였다. nudge의 지연 계수 1.5는 t번째 signer가 40 ms 안팎일 때 QUIC의 손실 복구보다 조금 먼저 ping을 보내 복구를 늦춘다.
4. p99의 흔들림에 관한 예측은 대부분 틀렸다. hedge와 first 가운데 5회의 차이가 5 ms를 넘은 것이 셋이다(t = 3의 hedge4 13.0 ms, t = 4의 hedge5 6.4 ms, threshold RSA의 first 16.8 ms). fastest의 차이가 20 ms를 넘은 것은 t = 4 하나였다(31.0 ms). t = 3에서 hedge4의 p99 평균은 28.6 ms로 예측 범위(23~27 ms)를 벗어났고 fastest는 70.7 ms로 범위 안이었다.
5. handshake의 예측은 절반이 맞았다. T_hs p99의 5회 평균은 first + nudge 48.6 ms, fastest 91.3 ms(FROST)와 95.8 ms(threshold RSA)로 범위 안이었다. hedge4는 55.5 ms(47.7~60.8 ms)로 예측(44~48 ms)보다 길었다. fastest의 5회 차이는 1.9 ms와 0.5 ms로, 20 ms 이상이라는 예측과 반대였다.
6. clientloss의 예측은 대체로 맞았다. 150 ms 넘게 늦어진 비율은 quic-go 1.70~2.45%, s2n-quic 1.80~2.50%, quiche 0.25~0.40%, aioquic 0.25~0.40%(지연 0, 20 ms)와 1.75%(100 ms)였다. quiche와 aioquic의 0.40%는 예측한 상한 0.3%를 넘었고, s2n-quic의 1.80%는 하한 2%에 못 미쳤다. s2n-quic에서 늦어진 handshake의 87~97%가 900 ms를 넘었다.

예측과 다른 결과에서 알게 된 것이다.

- hedge4와 first의 p99가 흔들린 까닭은 p99가 경계에 있기 때문이다. t = 3의 hedge4에서 30 ms를 넘긴 서명의 비율이 회마다 0.6~1.5%(합쳐서 0.98%)였다. 가까운 signer 넷 가운데 둘 이상이 손실을 겪을 확률을 계산하면 0.89%다. 1%와 거의 같으므로 회마다 p99가 "여유 signer로 끝난 값"(약 25 ms)과 "손실 복구를 기다린 값"(약 39 ms) 사이를 오간다. hedge5는 이 비율이 0.1~0.8%(합쳐서 0.52%)여서 p99가 26.3~26.6 ms로 일정했다. hedge5에서 비율이 낮은 까닭은 확인하지 않았다. 앞선 측정의 hedge4 24.7 ms는 낮은 쪽이 나온 한 회였다.
- fastest의 p99는 이번 측정에서 "2r + 27 ms"에 가까웠다(t = 3에서 68.7~73.0 ms). 앞선 측정의 85.6 ms와 100.7 ms는 host에 다른 부하가 있던 때의 값이다. 부하가 손실 복구를 늦추는 것으로 보이나 확인하지 않았다.
- 이번 측정의 중앙값은 앞선 측정보다 조금 길었다. handshake에서 T_hs − D_sign이 21.7~21.9 ms(앞선 측정 21.0~21.4 ms)였고, threshold RSA의 D_sign 중앙값이 25.8 ms(앞선 측정 24.2 ms)였다. host를 다시 시작한 뒤의 상태 차이로 보이며 원인은 확인하지 않았다.

hedge5를 실제 handshake로 재지 않았으므로 handshake 묶음에 hedge5를 더해 5회 측정한다. 예측은 다음과 같다. T_hs 중앙값은 43.5~45.0 ms, p99의 5회 평균은 47~52 ms이고 5회의 차이는 5 ms 이하다.

### 남은 일

- signer와 coordinator의 상호 인증을 구현하지 않았다. signer 연결의 TLS와 QUIC은 자체 서명 인증서를 쓰고 chain을 검증하지 않는다.
- 응답 없는 signer를 선택에서 빼고 다시 확인하는 기능이 없다. fastest는 죽은 signer를 계속 고르고, 한 번 느려져 빠진 signer의 회복을 알지 못한다.
- hedge 예산을 장애 때 푸는 규칙이 없다.
- signer 복제본, 분산 key 생성, nonce commitment와 presignature의 장애 뒤 관리를 구현하지 않았다.
- threshold ECDSA는 GG20만 측정했다. 집합을 고정한 조건과 더 새로운 protocol을 측정하지 않았다.
- 모델에 QUIC의 긴 손실 복구와 TCP의 연쇄 지연이 없다. "여유 signer" 규칙을 다른 배치로 검증하지 않았다.
- s2n-quic 서버가 손실된 ServerHello를 다시 보내는 시점, 두 번째 client에서 handshake가 겹칠 때 1초가 걸리는 원인, quiche 서버의 동시 100개 조건에서 p99가 튄 원인을 확인하지 않았다.
- signer에 물리 core를 하나씩 준 조건을 측정하지 못했다. signer 5개 가운데 둘이 물리 core를 나누어 쓴다.
- TCP의 손실 복구 동작과 congestion window를 packet 단위로 확인하지 않았다. QUIC은 coordinator 쪽 qlog로 확인했다.
- quic-go가 아닌 QUIC 구현체를 signer 연결에 쓰지 않았다. congestion control과 timer의 초깃값이 구현체마다 다르다.
- 0-RTT QUIC이 handshake가 몰릴 때 30~42 ms 느린 원인을 분해하지 않았다.
- nudge의 지연 시간에 signer 계산 시간을 반영하지 않았다. RTT_s의 jitter가 크면 nudge가 서명마다 발동한다.
- 실제 WAN에서 측정하지 않았다.
