# Phase 2 결과: threshold signer

측정일은 2026년 11월 13일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 가설은 같은 문서 5절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 구현 결정은 [worklog.md](worklog.md)에 있다. 원자료는 `results/2026-11-13-phase2-*`에 있다.

## 1. 측정 조건

| 항목 | 값 |
| --- | --- |
| host | Phase 1과 같은 host다. 측정 중 1분 load average는 1.9~3.8이었다. |
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
5. signer마다 RTT_s가 다른 배치에서 loss가 없을 때의 D_sign 중앙값은 선택된 signer 중 가장 느린 signer의 RTT_s와 왕복 수의 곱으로 예측됐다. signer 링크에 loss 2%를 주면 선택한 signer 전부의 응답을 기다리는 구성은 p95가 170~310 ms 늘었고, n개 전체에 요청해 먼저 온 t개를 쓰는 threshold RSA 구성은 p95가 27.1 ms(t = 2)와 68.9 ms(t = 3)에 머물렀다.

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

## 5. signer 배치와 선택 정책

signer 5개의 RTT_s를 signer 번호 순서대로 50, 5, 100, 1, 20 ms로 두었다. 서명을 하나씩 순서대로 요청했고 연결은 유지하는 방식이며 조건마다 400회다. 28개 조건 모두에서 400회가 전부 성공했다. loss는 coordinator와 signer의 송신 방향에 각각 주었다.

선택 정책은 세 가지다.

| 정책 | 동작 | 이 배치에서 쓰는 signer |
| --- | --- | --- |
| fixed | 설정 순서의 앞 t개를 쓴다. | t = 2이면 RTT_s 50·5 ms, t = 3이면 50·5·100 ms |
| fastest | 요청 소요 시간의 이동 평균이 가장 짧은 t개를 쓴다. 선택되지 않은 signer의 평균은 갱신되지 않는다. | loss가 없으면 t = 2일 때 1·5 ms, t = 3일 때 1·5·20 ms |
| first | n개 전체에 요청하고 먼저 온 t개의 share를 결합한다. share가 signer 집합에 묶이지 않는 threshold RSA에서만 쓴다. | 응답 순서에 따라 달라진다. |

"모델" 열은 [experiment-design.md](experiment-design.md) 4절 모델의 네트워크 항, 곧 왕복 수에 선택된 signer 중 가장 큰 RTT_s를 곱한 값(ms)이다. fastest와 first는 RTT_s가 가장 짧은 t개가 선택된다고 보고 계산했다. 나머지 칸은 D_sign의 중앙값 / p95 / p99(ms)다.

### 5.1 (t, n) = (2, 5)

| scheme | 정책 | 모델 | loss 0% | loss 2% |
| --- | --- | --- | --- | --- |
| frost | fixed | 100 | 100.50 / 100.87 / 101.74 | 100.53 / 359.32 / 480.51 |
| frost | fastest | 10 | 10.44 / 11.86 / 11.98 | 10.49 / 224.18 / 246.27 |
| frost-pre | fixed | 50 | 50.32 / 50.60 / 53.04 | 50.33 / 220.89 / 319.75 |
| frost-pre | fastest | 5 | 5.29 / 5.86 / 6.43 | 20.28 / 221.72 / 257.79 |
| rsa | fixed | 50 | 54.09 / 54.71 / 55.37 | 54.09 / 55.97 / 317.03 |
| rsa | fastest | 5 | 9.15 / 9.87 / 11.50 | 24.04 / 219.69 / 256.94 |
| rsa | first | 5 | 10.30 / 10.52 / 10.77 | 9.37 / 27.14 / 27.40 |

### 5.2 (t, n) = (3, 5)

| scheme | 정책 | 모델 | loss 0% | loss 2% |
| --- | --- | --- | --- | --- |
| frost | fixed | 200 | 200.64 / 201.93 / 203.06 | 200.68 / 508.51 / 616.96 |
| frost | fastest | 40 | 40.56 / 41.04 / 42.08 | 40.60 / 272.13 / 467.67 |
| frost-pre | fixed | 100 | 100.44 / 100.90 / 101.93 | 100.46 / 402.79 / 440.25 |
| frost-pre | fastest | 20 | 20.37 / 20.56 / 21.28 | 20.42 / 243.29 / 316.03 |
| rsa | fixed | 100 | 105.20 / 105.70 / 106.37 | 104.22 / 315.76 / 421.24 |
| rsa | fastest | 20 | 23.98 / 24.38 / 24.94 | 57.11 / 259.46 / 326.85 |
| rsa | first | 20 | 27.10 / 27.37 / 27.59 | 27.12 / 68.88 / 105.23 |

### 5.3 관찰

**loss가 없을 때.** D_sign 중앙값은 모델의 네트워크 항에 scheme별 계산 시간을 더한 값과 맞았다. 중앙값에서 모델 값을 뺀 차이는 FROST 구성에서 0.3~0.6 ms, threshold RSA 구성에서 4.0~7.1 ms였다. 같은 t에서 중앙값이 가장 짧은 구성은 frost-pre에 fastest 정책을 쓴 구성이었다(t = 2에서 5.29 ms, t = 3에서 20.37 ms). t를 2에서 3으로 올리면 fastest 정책의 D_sign은 세 번째로 가까운 signer의 RTT_s가 정했다.

**loss 2%, 선택한 signer 전부를 기다리는 구성.** fixed와 fastest 정책에서는 선택한 t개의 응답이 모두 와야 서명이 완성된다. 이 구성들의 p95는 loss가 없을 때보다 170~310 ms 길었다. 예외는 rsa·fixed·(2, 5)로, p95는 1.3 ms만 늘었고 p99가 317 ms였다. 늘어난 크기는 Linux TCP의 최소 재전송 timeout 200 ms와 같은 범위이며, packet 단위로 재전송을 확인하지는 않았다. 이 지연을 줄이는 방법은 [results-phase3-adaptive.md](results-phase3-adaptive.md)에서 측정했다.

**loss 2%, first 정책.** threshold RSA에 first 정책을 쓰면 p95가 t = 2에서 27.14 ms, t = 3에서 68.88 ms였다. 가장 가까운 signer의 응답이 늦어지면 다음으로 가까운 signer의 share가 대신 쓰였다. t = 2에서 RTT_s 20 ms인 signer의 share가 400회 중 28회, 50 ms인 signer의 share가 2회 쓰였다. 이 정책은 서명마다 n개 signer가 모두 share를 계산하므로 signer 계산량이 t개만 쓰는 정책의 n/t배다.

**loss 2%, fastest 정책.** fastest 정책은 loss가 있을 때 중앙값도 나빠졌다. frost-pre (2, 5)의 중앙값은 5.29 ms에서 20.28 ms로, rsa (3, 5)의 중앙값은 23.98 ms에서 57.11 ms로 늘었다. rsa (3, 5)에서는 RTT_s 5 ms인 signer가 400회 내내 선택되지 않고 50 ms인 signer가 대신 쓰였다. 재전송으로 늦어진 응답 하나가 그 signer의 이동 평균을 더 먼 signer보다 크게 만들고, 선택되지 않는 동안에는 평균이 갱신되지 않기 때문이다. frost (2, 5)에서는 같은 정책이 가까운 두 signer를 400회 모두 유지했다.

### 5.4 loss 2%에서 느린 서명의 비율

D_sign이 같은 구성의 loss 없는 중앙값보다 100 ms 넘게 긴 서명의 비율이다. "예상" 열은 서명 하나가 기다리는 요청과 응답 packet 2 × R × t개 중 하나 이상이 손실될 확률 1 − 0.98^(2Rt)이다.

| scheme | R | t | 예상 | fixed | fastest | first |
| --- | --- | --- | --- | --- | --- | --- |
| frost | 2 | 2 | 14.9% | 14.8% | 16.0% | |
| frost | 2 | 3 | 21.5% | 21.2% | 18.2% | |
| frost-pre | 1 | 2 | 7.8% | 7.8% | 7.5% | |
| frost-pre | 1 | 3 | 11.4% | 13.0% | 13.2% | |
| rsa | 1 | 2 | 7.8% | 5.0% | 5.8% | 0.0% |
| rsa | 1 | 3 | 11.4% | 15.0% | 11.2% | 0.2% |

선택한 signer 전부를 기다리는 구성에서 느린 서명의 비율은 왕복 수와 t에 따라 늘었고 예상 값과 4%p 안에서 맞았다. first 정책에서는 800회 중 1회만 느렸다.

## 6. 가설 판정

| 가설 | 예측 | 판정 |
| --- | --- | --- |
| H2 | RTT_s를 올리면 D_sign의 증가량은 왕복 수와 RTT_s 증가량의 곱에 가깝다. precomputation을 켠 FROST의 증가량은 켜지 않은 FROST의 절반에 가깝다. | 예측대로다. RTT_s 0 ms에서 50 ms로 올렸을 때 증가량은 frost (2, 3) 100.11 ms, frost-pre (2, 3) 50.03 ms, rsa (2, 3) 50.92 ms였다. |
| H2의 앞부분 | RTT_s가 1 ms 미만이면 D_sign은 signer의 계산 시간에 좌우된다. | threshold RSA에서는 맞았다(93~95%). FROST에서는 signer 계산이 D_sign 중앙값의 절반에 못 미쳤다. |
| H4 | 모델이 예측한 상위 3개 구성 안에 실측 p99가 가장 낮은 구성이 들어간다. | 5절의 네 경우(t = 2·3, loss 0·2%)에서 예측과 어긋나지 않았다. 다만 이 측정은 D_sign을 한 가지 배치에서 본 것이고, loss가 있을 때 first 정책이 가장 좋은 이유를 loss를 넣지 않은 모델은 설명하지 못한다. H4의 판정은 Phase 3으로 미룬다. |

Phase 1의 결론인 "T_hs는 지연 없는 handshake 시간과 D_sign의 합"은 실제 signer에서도 유지됐다(4.1절).

## 7. 한계

- coordinator와 signer 사이의 연결은 TLS를 쓰지 않는 TCP다. 실제 배치에서는 상호 인증이 필요하며 연결 수립 비용이 늘어난다.
- signer마다 CPU 하나만 썼고 signer의 복제본을 두지 않았다. 4.2절의 대기 시간은 이 조건에 한정된다.
- FROST library의 호출을 process 안에서 직렬화했다. 동시 호출이 안전한 구현에서는 4.2절의 FROST 수치가 달라질 수 있다.
- RSA key는 safe prime이 아니며 threshold ECDSA는 측정하지 않았다. 공개 WebPKI에서 쓸 수 있는 scheme 가운데 측정한 것은 threshold RSA뿐이다.
- nonce commitment를 signer의 memory에만 보관했다. 장애 뒤에도 재사용을 막는 저장 비용은 측정에 들어 있지 않다.
- signer 배치는 한 가지만 측정했고 RTT_s의 jitter는 주지 않았다. loss 조건은 400회여서 p99는 네 번째로 느린 서명 하나가 정한다.
- fastest 정책은 이 실험용으로 만든 단순한 구현이다. 선택되지 않은 signer를 주기적으로 측정하는 정책은 시험하지 않았다.
- host에서 실험과 무관한 프로세스가 CPU를 쓰고 있었다. RTT_s 0 ms 조건의 1 ms 미만 차이는 비교에 쓰지 않는다.

## 8. 설계에 주는 영향

- D_sign의 네트워크 부분은 "왕복 수 × 선택된 signer 중 가장 큰 RTT_s"로 모델링한다. 요청마다 연결을 새로 맺으면 왕복 수에 연결 수립의 왕복을 더한다.
- 지연을 줄이는 수단의 효과는 측정한 범위에서 다음 순서였다. signer 선택(먼 signer를 피함), precomputation(왕복 수를 절반으로), 연결 유지(연결 수립 왕복 제거)다. t를 올리는 비용은 t번째로 가까운 signer까지의 RTT_s다.
- 꼬리 지연은 loss가 정하며, 선택한 signer 전부를 기다리는 구성에서 느린 서명의 비율은 loss율 p에 대해 1 − (1 − p)^(2Rt)로 근사된다. 모델에 이 항을 넣는다. FROST는 share가 signer 집합에 묶여 first 정책을 쓸 수 없으므로, 서로 다른 signer 집합으로 여러 세션을 동시에 진행하는 방식과 ROAST[14]를 Phase 3의 후보로 둔다.
- signer의 계산 시간은 단일 서명의 지연보다 처리 용량에서 드러난다. Phase 3의 최적화 문제에 signer별 처리율 제약을 넣는다.
- signer 선택 정책에는 선택되지 않은 signer의 RTT_s를 갱신하는 절차와, 재전송으로 생긴 이상값에 흔들리지 않는 추정이 필요하다.
