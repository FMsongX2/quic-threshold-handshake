# Phase 2 결과: threshold signer

측정일은 2026년 11월 13일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 가설은 같은 문서 5절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 구현 결정은 [worklog.md](worklog.md)에 있다. 원자료는 `results/2026-11-13-phase2-*`에 있다.

## 1. 측정 조건

| 항목 | 값 |
| --- | --- |
| host | Phase 1과 같은 host다. 측정 중 1분 load average는 2.2~3.8이었다. |
| signature scheme | FROST(Ed25519, SHA-512), Shoup threshold RSA-2048(PSS, SHA-256), 비교 기준인 plain Ed25519와 plain RSA-2048 |
| library | `github.com/bytemare/frost` commit 316cadd, `github.com/cloudflare/circl` v1.6.5 `tss/rsa`, Go 1.26 |
| coordinator | Go process. 서명 지연 측정에서는 단독 benchmark, handshake 측정에서는 quic-go v0.65.0 서버 안에서 인증서 key로 동작한다. CPU 4·5번에 고정했다. |
| signer | signer마다 container 하나, CPU 하나에 고정했다. |
| 링크 | coordinator와 signer는 Docker bridge network에서 TCP로 통신한다. RTT_s는 signer container의 송신 방향에 `tc netem` 지연으로 주었다. RTT_s 0 ms는 netem을 걸지 않은 조건이다. |
| 반복 | 서명 지연 측정은 warm-up 5회 뒤 조건마다 200회 또는 400회, handshake 측정은 warm-up 1회 뒤 50회 또는 500회 |

이 문서의 scheme 이름은 다음을 뜻한다.

| 이름 | 뜻 | ClientHello 이후 왕복 수 R |
| --- | --- | --- |
| plain-ed25519, plain-rsa | 전체 key를 가진 signer 하나가 서명한다. (t, n) = (1, 1)이다. | 1 |
| frost | FROST의 두 round(commit, sign)를 서명 요청마다 수행한다. | 2 |
| frost-pre | coordinator가 nonce commitment를 미리 받아 두고 서명 요청 때는 sign round만 수행한다. | 1 |
| rsa | signer가 padding된 값에 대한 signature share를 계산하고 coordinator가 결합한다. | 1 |

coordinator는 결합한 서명을 표준 검증기(`ed25519.Verify`, `rsa.VerifyPSS`)로 검증한 뒤 반환하며, 이 검증 시간은 D_sign에 들어 있다. D_sign은 다음 세 부분으로 나눴다.

- signer 계산: round마다 signer들이 보고한 처리 시간 중 최댓값을 구해 round에 걸쳐 더한 값
- 네트워크와 대기: round별 소요 시간의 합에서 signer 계산을 뺀 값
- coordinator 처리: D_sign에서 round별 소요 시간의 합을 뺀 값. padding, share 결합, 서명 검증이 들어간다.

## 2. 요약

1. 서명 지연은 왕복 수와 RTT_s의 곱으로 정해졌다. RTT_s를 0 ms에서 50 ms로 올리면 D_sign 중앙값은 frost에서 100.1 ms, frost-pre에서 50.0 ms, rsa에서 50.0~50.9 ms 늘었다. 요청마다 TCP 연결을 새로 맺으면 증가량이 두 배가 됐다.
2. signer의 계산 시간은 scheme에 따라 한 자릿수 이상 차이 났다. signature share 하나를 계산하는 시간의 중앙값은 FROST에서 0.1~0.2 ms, threshold RSA에서 3.9~4.9 ms였다.
3. 실제 signer를 붙인 handshake에서도 T_hs는 지연 없는 handshake 시간과 D_sign의 합이었다. 10개 조건에서 T_hs 중앙값과 D_sign 중앙값의 차이는 21.0~21.2 ms로, Phase 1에서 측정한 지연 없는 handshake 시간과 같았다.
4. handshake 100개가 동시에 시작되면 signer의 계산 시간이 대기 시간으로 누적됐다. RTT_s 5 ms에서 D_sign 중앙값은 threshold RSA (2, 3)에서 9.1 ms에서 257.9 ms로, frost-pre (2, 3)에서 5.4 ms에서 11.0 ms로 늘었다.

## 3. 서명 지연의 분해

조건은 서명을 하나씩 순서대로 요청, loss 0%, 모든 signer의 RTT_s가 같음, 설정 순서의 앞 t개 signer 사용, 조건마다 200회다. 48개 조건 모두에서 200회가 전부 성공했다.

### 3.1 연결을 유지할 때

D_sign 열의 각 칸은 중앙값(p95)이고 단위는 ms다. 오른쪽 두 열은 RTT_s 0 ms 조건의 중앙값이다.

| scheme (t, n) | R | RTT_s 0 ms | RTT_s 5 ms | RTT_s 50 ms | signer 계산 | coordinator 처리 |
| --- | --- | --- | --- | --- | --- | --- |
| plain-ed25519 (1, 1) | 1 | 0.11 (0.32) | 5.13 (5.36) | 50.14 (50.72) | 0.02 | 0.03 |
| frost (2, 3) | 2 | 0.43 (1.31) | 10.47 (11.64) | 100.54 (101.61) | 0.20 | 0.13 |
| frost-pre (2, 3) | 1 | 0.28 (0.76) | 5.30 (5.89) | 50.31 (50.93) | 0.11 | 0.12 |
| frost (3, 5) | 2 | 0.85 (1.88) | 11.82 (12.39) | 101.85 (102.98) | 0.24 | 0.16 |
| frost-pre (3, 5) | 1 | 0.38 (0.87) | 5.48 (6.32) | 50.46 (51.79) | 0.16 | 0.15 |
| plain-rsa (1, 1) | 1 | 0.63 (0.84) | 5.65 (5.87) | 50.67 (51.05) | 0.54 | 0.02 |
| rsa (2, 3) | 1 | 4.23 (5.24) | 9.38 (11.75) | 55.16 (55.59) | 3.92 | 0.13 |
| rsa (3, 5) | 1 | 5.20 (5.91) | 10.21 (10.43) | 55.18 (56.78) | 4.94 | 0.20 |

RTT_s가 0 ms일 때 threshold RSA의 D_sign은 signer 계산이 93~95%를 차지했다. FROST에서는 signer 계산이 0.11~0.24 ms, coordinator 처리가 0.12~0.16 ms로 둘 다 1 ms 미만이었다. RTT_s가 5 ms 이상이면 모든 scheme에서 네트워크와 대기가 가장 큰 부분이 됐다.

frost (3, 5)는 frost (2, 3)보다 RTT_s 5 ms에서 1.35 ms, 50 ms에서 1.31 ms 길었다. signer별 응답 시간을 보면 세 번째 signer의 응답이 다른 두 signer보다 round마다 0.5~0.8 ms 늦었다. 같은 signer를 쓰는 frost-pre (3, 5)에서는 이 차이가 없었다. 원인은 확인하지 못했으며, host에서 함께 실행 중인 프로세스의 영향일 수 있다.

### 3.2 요청마다 연결을 새로 맺을 때

각 칸은 D_sign 중앙값(ms)이다.

| scheme (t, n) | RTT_s 0 ms | RTT_s 5 ms | RTT_s 50 ms |
| --- | --- | --- | --- |
| plain-ed25519 (1, 1) | 0.17 | 10.19 | 100.23 |
| frost (2, 3) | 0.51 | 20.63 | 200.74 |
| frost-pre (2, 3) | 0.35 | 10.41 | 100.44 |
| frost (3, 5) | 0.91 | 21.83 | 201.49 |
| frost-pre (3, 5) | 0.45 | 10.51 | 100.54 |
| plain-rsa (1, 1) | 0.73 | 10.75 | 100.78 |
| rsa (2, 3) | 4.25 | 14.86 | 104.28 |
| rsa (3, 5) | 5.34 | 15.35 | 105.35 |

TCP 연결 수립이 round마다 왕복 한 번을 더해, RTT_s에 비례하는 부분이 연결을 유지할 때의 두 배가 됐다. 이 측정의 연결은 TLS를 쓰지 않는다. TLS 1.3을 쓰면 연결마다 왕복이 한 번 더 든다.

### 3.3 RTT_s에 따른 증가량

RTT_s를 0 ms에서 50 ms로 올렸을 때 D_sign 중앙값의 증가량(ms)이다.

| scheme (t, n) | R | 연결 유지 | 요청마다 새 연결 |
| --- | --- | --- | --- |
| plain-ed25519 (1, 1) | 1 | 50.03 | 100.06 |
| frost (2, 3) | 2 | 100.11 | 200.23 |
| frost-pre (2, 3) | 1 | 50.03 | 100.09 |
| frost (3, 5) | 2 | 101.00 | 200.58 |
| frost-pre (3, 5) | 1 | 50.09 | 100.09 |
| plain-rsa (1, 1) | 1 | 50.05 | 100.05 |
| rsa (2, 3) | 1 | 50.92 | 100.03 |
| rsa (3, 5) | 1 | 49.98 | 100.01 |

## 4. 실제 signer를 붙인 handshake

quic-go 서버의 인증서 key를 coordinator로 바꾸고 quic-go client로 handshake 시간을 쟀다. RTT_c는 20 ms, loss는 0%, signer와의 연결은 유지하는 방식이다. D_sign은 서버 안의 coordinator가 기록한 값이다.

### 4.1 handshake를 하나씩 할 때

조건마다 50회이며 모두 성공했다. 단위는 ms다.

| scheme (t, n) | RTT_s | D_sign 중앙값 | T_hs 중앙값 | T_hs p95 | T_hs 중앙값 − D_sign 중앙값 |
| --- | --- | --- | --- | --- | --- |
| plain-ed25519 (1, 1) | 5 | 5.15 | 26.18 | 26.76 | 21.04 |
| plain-ed25519 (1, 1) | 50 | 50.17 | 71.18 | 71.66 | 21.01 |
| frost (2, 3) | 5 | 10.54 | 31.62 | 32.99 | 21.08 |
| frost (2, 3) | 50 | 100.58 | 121.76 | 122.98 | 21.18 |
| frost-pre (2, 3) | 5 | 5.36 | 26.40 | 28.06 | 21.04 |
| frost-pre (2, 3) | 50 | 50.33 | 71.40 | 71.77 | 21.08 |
| plain-rsa (1, 1) | 5 | 5.68 | 26.71 | 27.38 | 21.03 |
| plain-rsa (1, 1) | 50 | 50.69 | 71.73 | 72.12 | 21.04 |
| rsa (2, 3) | 5 | 9.13 | 30.14 | 31.30 | 21.00 |
| rsa (2, 3) | 50 | 54.12 | 75.19 | 76.70 | 21.08 |

마지막 열은 21.00~21.18 ms다. Phase 1에서 quic-go 서버의 지연 없는 T_hs 중앙값은 21.01 ms였다([results-phase1-pilot.md](results-phase1-pilot.md) 4절). Phase 1의 인증서는 ECDSA P-256이었고 이번 측정의 인증서는 Ed25519와 RSA-2048이다.

### 4.2 handshake 100개를 동시에 시작할 때

RTT_s는 5 ms다. client가 handshake 100개를 같은 시각에 시작하는 것을 5회 반복해 500개를 측정했고 모두 성공했다. 단위는 ms다. "하나씩" 열은 4.1절의 같은 조건 값이다.

| scheme (t, n) | D_sign 중앙값 (하나씩) | D_sign 중앙값 | D_sign p95 | T_hs 중앙값 | T_hs p95 |
| --- | --- | --- | --- | --- | --- |
| plain-ed25519 (1, 1) | 5.15 | 5.20 | 7.84 | 29.32 | 33.81 |
| frost (2, 3) | 10.54 | 16.39 | 20.37 | 40.19 | 45.09 |
| frost-pre (2, 3) | 5.36 | 11.04 | 17.20 | 35.98 | 46.24 |
| rsa (2, 3) | 9.13 | 257.87 | 476.38 | 284.29 | 501.22 |

threshold RSA signer는 CPU 하나에서 signature share 하나에 약 4~5 ms를 쓴다. 요청 100개가 한꺼번에 오면 마지막 요청은 앞선 요청들의 계산이 끝나기를 기다리며, D_sign의 p95인 476 ms는 요청 100개의 계산 시간 합과 같은 크기다. FROST signer는 share 하나에 0.1~0.2 ms를 쓰지만, 이 구현은 FROST library 호출을 process 안에서 직렬화하므로([worklog.md](worklog.md)) 동시 요청에서 D_sign이 5~6 ms 늘었다. frost-pre에서는 coordinator가 commitment를 보충하는 요청도 같은 signer에서 직렬로 처리된다.

이 측정에서 signer는 CPU 하나만 쓴다. signer에 CPU를 더 주거나 signer를 여러 개의 복제본으로 두면 대기 시간이 줄어들 수 있으나 측정하지 않았다.
