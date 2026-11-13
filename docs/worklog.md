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
