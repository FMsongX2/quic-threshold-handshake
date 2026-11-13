# Phase 1 pilot 결과: 서명 지연 주입

측정일은 2026년 11월 13일이다. 용어와 기호는 [problem-definition.md](problem-definition.md) 2절, 가설은 같은 문서 5절, 실험 설계는 [experiment-design.md](experiment-design.md)를 따른다. 원자료는 `results/2026-11-13-pilot-*`에 있다.

## 1. 측정 조건

| 항목 | 값 |
| --- | --- |
| host | x86-64 CPU 8 core 16 thread(AMD Ryzen 7 9700X), RAM 30 GB, Ubuntu 24.04, kernel 7.0. 측정 중 1분 load average는 1.6~2.8이었다. |
| 서버 | quic-go v0.65.0(Go 1.26), aioquic 1.8.0(Python 3.12), Cloudflare quiche 0.30.1(boring 5.5.0) |
| client | quic-go v0.65.0. session resumption을 쓰지 않아 모든 연결이 전체 handshake를 수행한다. |
| 인증서 | ECDSA P-256 자체 서명 인증서 1장. 서명 알고리즘은 ecdsa_secp256r1_sha256이다. |
| 링크 | Docker bridge network. 두 container의 송신 방향에 `tc netem`으로 10 ms씩 지연을 주어 RTT_c를 20 ms로 맞췄다. |
| CPU 고정 | 서버 container는 CPU 4·5번, client container는 6·7번. 동시 handshake 측정에서는 client에 6·7·14·15번을 주었다. |
| 반복 | 조건마다 warm-up handshake 1회를 버린 뒤 측정했다. 측정 횟수는 각 절에 적었다. |

서명 지연은 구현체의 서명 hook에 넣었다. 세 서버 모두 local key로 서명을 먼저 계산하고, 지정한 시간이 지난 뒤 결과를 TLS 계층에 돌려준다.

| 서버 | 지연을 넣은 위치 | 지연 중 서버의 상태 |
| --- | --- | --- |
| quic-go | `crypto.Signer.Sign`이 반환 전에 sleep한다. | 그 연결의 handshake를 처리하는 goroutine이 멈춘다. 다른 연결은 각자의 goroutine에서 진행한다. |
| aioquic | 인증서 key의 `sign()`이 반환 전에 `time.sleep`을 호출한다. | 모든 연결을 처리하는 asyncio event loop가 멈춘다. |
| quiche | BoringSSL private key method의 `sign`이 retry를 반환하고, 지정한 시간이 지난 뒤 `complete`가 서명을 돌려준다. | event loop가 계속 돌며 다른 packet을 처리한다. 재개 timer의 해상도는 1 ms다. |

T_hs는 client가 연결 함수를 호출한 시각부터 그 함수가 반환한 시각까지로 쟀다. 지연 sweep의 1,650개 handshake에서 이 값은 qlog의 첫 packet 송신부터 handshake complete까지의 시간보다 0.19~1.15 ms(중앙값 0.38 ms) 길었다.

## 2. 요약

1. 단일 handshake에서 서명 지연은 T_hs에 그대로 더해졌다. 지연 0~1,200 ms, 서버 3종에서 T_hs 중앙값은 지연이 없을 때의 중앙값과 주입 지연의 합에서 0.85 ms 이상 벗어나지 않았다. 지연이 client의 PTO보다 길어 probe packet이 나간 조건에서도 같았다.
2. 서명을 기다리는 동안의 서버 동작은 구현체마다 달랐다. quic-go 서버는 서명이 끝날 때까지 대부분 아무 packet도 보내지 않았고, aioquic은 ACK만, quiche는 ServerHello까지 먼저 보냈다. 이 차이는 T_hs를 바꾸지 않았지만 client의 첫 RTT sample과 probe packet 수를 바꿨다.
3. 동시 handshake에서는 구현체의 실행 모델이 T_hs를 갈랐다. 서명 지연 50 ms, 동시 handshake 100개에서 T_hs 중앙값은 quic-go 74 ms, quiche 73 ms, aioquic 3,228 ms였다.

## 3. 서명을 기다리는 동안의 서버 동작

아래 표는 지연 sweep(동시 handshake 1개, loss 0%, 조건마다 50회)의 client qlog에서 얻었다. 값은 주입 지연 300 ms 조건의 중앙값 또는 평균이다.

| 서버 | 서버의 첫 packet이 도착한 시각 | 첫 packet의 내용 | client의 첫 RTT sample | handshake complete 전에 client가 보낸 packet 수 |
| --- | --- | --- | --- | --- |
| quic-go | 320.9 ms | Initial(ACK, ServerHello) | 120.4 ms | 4.20 |
| aioquic | 20.5 ms | Initial(ACK, PING) | 20.5 ms | 7.00 |
| quiche | 20.4 ms | Initial(ACK, ServerHello) | 20.3 ms | 5.02 |

지연이 없을 때 handshake complete 전에 client가 보낸 packet 수는 quic-go 서버 2.04개, aioquic 3개, quiche 2개였다. quic-go client의 ClientHello는 Initial packet 2개에 나뉘어 나갔다.

**quic-go 서버.** 서명이 끝날 때까지 ServerHello를 보내지 않았다. client는 RTT sample 없이 기다리므로 quic-go client의 초기 PTO인 200 ms가 지나면 ClientHello를 다시 보냈다. 주입 지연이 200 ms 미만이면 client의 첫 RTT sample은 실제 RTT에 주입 지연이 더해진 값이 됐다. 지연 50·100·150 ms에서 첫 RTT sample의 중앙값은 70.8·120.7·170.7 ms였다. 지연이 200 ms 이상이면 서버의 ACK가 마지막 probe packet을 확인하므로 sample은 그 probe 이후의 대기 시간이 됐다(지연 300 ms에서 120.4 ms, 1,200 ms에서 620.0 ms). 지연 50~150 ms 조건의 6~16%에서는 서버가 ClientHello의 첫 packet에 ACK만 담은 packet을 먼저 보냈고, 이때는 RTT sample이 20 ms대로 잡혔다.

**aioquic.** ClientHello의 첫 packet을 받은 직후 ACK와 PING을 보냈고, 둘째 packet으로 ClientHello가 완성된 뒤 서명 호출에서 멈췄다. client는 20 ms대의 RTT sample을 얻어 PTO가 약 61 ms로 줄었고, 주입 지연 50 ms 이상의 모든 handshake에서 PTO가 만료돼 ClientHello를 다시 보냈다. event loop가 멈춰 있으므로 서버는 이 재전송에 응답하지 않았다. 이 동작은 ClientHello가 packet 2개에 나뉜 경우에 관측한 것이다.

**quiche.** 서명을 요청한 직후 ACK와 ServerHello를 보냈다. client는 20 ms대의 RTT sample과 handshake key를 먼저 얻었다. client는 PTO마다 PING을 담은 Initial packet을 보냈고 서버는 서명을 기다리는 중에도 ACK로 응답했다.

## 4. 단일 handshake에서 서명 지연의 전달

조건은 동시 handshake 1개, loss 0%, 조건마다 50회다. 각 칸은 T_hs 중앙값(ms)이고, 괄호 안은 그 값에서 같은 서버의 지연 0 ms 중앙값과 주입 지연을 뺀 값(ms)이다.

| 주입 지연(ms) | quic-go | aioquic | quiche |
| --- | --- | --- | --- |
| 0 | 21.01 | 21.58 | 20.92 |
| 5 | 26.20 (0.19) | 26.76 (0.18) | 25.90 (-0.02) |
| 20 | 41.23 (0.22) | 41.67 (0.09) | 40.87 (-0.05) |
| 50 | 71.86 (0.85) | 71.82 (0.24) | 71.25 (0.33) |
| 100 | 121.41 (0.40) | 121.83 (0.25) | 121.48 (0.56) |
| 150 | 171.43 (0.42) | 171.71 (0.13) | 171.66 (0.74) |
| 200 | 221.33 (0.32) | 221.86 (0.28) | 221.57 (0.65) |
| 250 | 271.42 (0.41) | 271.82 (0.24) | 271.65 (0.73) |
| 300 | 321.47 (0.46) | 321.96 (0.38) | 321.21 (0.29) |
| 500 | 521.67 (0.66) | 521.99 (0.41) | 521.43 (0.51) |
| 1200 | 1221.78 (0.77) | 1222.16 (0.58) | 1221.48 (0.56) |

33개 조건 모두에서 50회가 전부 성공했다. 괄호 안 값의 절댓값은 최대 0.85 ms였고, 한 조건 안에서 T_hs의 최댓값과 중앙값의 차이는 최대 3.38 ms였다. [experiment-design.md](experiment-design.md) 4절 모델의 $\Delta$는 이 범위에서 1 ms 미만이다.

client가 보낸 probe packet은 T_hs를 늘리지 않았다. 서버가 서명을 끝낸 뒤 보내는 응답은 probe와 무관하게 같은 시각에 도착했기 때문이다. probe의 비용은 추가 packet으로 나타났다. handshake complete 전에 client가 보낸 packet 수의 평균은 주입 지연 1,200 ms에서 quic-go 서버 6.30개, aioquic 11개, quiche 7개였다.

## 5. 동시 handshake

client가 같은 시각에 여러 연결을 시작한 조건이다. 동시 handshake 10개는 5회 반복해 50개, 100개는 5회 반복해 500개를 측정했다. 각 칸은 T_hs의 중앙값 / p95 / 최댓값(ms)이다.

| 서버 | 주입 지연(ms) | 동시 1개 | 동시 10개 | 동시 100개 |
| --- | --- | --- | --- | --- |
| quic-go | 0 | 21.08 / 21.48 / 21.89 | 21.85 / 22.38 / 22.39 | 25.77 / 31.52 / 49.76 |
| quic-go | 50 | 71.76 / 72.49 / 74.92 | 72.29 / 73.10 / 73.35 | 74.39 / 79.68 / 90.68 |
| aioquic | 0 | 21.38 / 21.92 / 22.65 | 24.18 / 27.05 / 27.74 | 48.88 / 254.23 / 632.20 |
| aioquic | 50 | 71.60 / 72.13 / 72.63 | 277.20 / 529.98 / 532.32 | 3228.43 / 6381.36 / 6681.47 |
| quiche | 0 | 20.89 / 21.14 / 21.21 | 21.70 / 22.22 / 22.27 | 31.53 / 36.50 / 221.91 |
| quiche | 50 | 71.15 / 71.73 / 71.82 | 71.56 / 72.10 / 72.17 | 72.70 / 76.48 / 273.23 |

aioquic의 동시 100개·지연 50 ms 조건에서는 500개 중 4개가 30초 제한 시간 안에 끝나지 않았다. 나머지 조건은 모두 성공했다.

aioquic은 서명을 하나씩 순서대로 처리했다. 동시 10개·지연 50 ms의 첫 반복에서 10개 handshake의 T_hs는 72, 123, 174, 225, 276, 327, 378, 429, 479, 530 ms로, 약 51 ms 간격이었다. quic-go는 서명 hook이 동기형이지만 연결마다 goroutine이 따로 있어 서명 대기가 다른 연결을 막지 않았다. quiche는 event loop 하나로 모든 연결을 처리하지만 서명 대기 중에 loop가 멈추지 않았다.

지연이 0 ms일 때도 동시 100개에서는 세 서버 모두 T_hs가 늘었다. 이 증가에는 서버와 client의 계산 시간이 함께 들어 있으며 둘을 분리해 측정하지 않았다.

## 6. loss가 있을 때

조건은 동시 handshake 1개, 방향마다 loss 5%, 조건마다 200회다. 각 칸은 T_hs의 중앙값 / p95 / 최댓값(ms)이다.

| 서버 | 지연 0 ms | 지연 50 ms | 지연 300 ms |
| --- | --- | --- | --- |
| quic-go | 21.06 / 221.18 / 245.37 | 71.44 / 271.17 / 392.79 | 321.48 / 521.60 / 582.51 |
| aioquic | 21.49 / 45.41 / 421.97 | 71.78 / 133.06 / 277.97 | 321.91 / 384.60 / 1488.81 |
| quiche | 20.93 / 82.23 / 143.26 | 71.24 / 132.44 / 1020.42 | 321.57 / 383.34 / 1066.61 |

9개 조건 모두에서 200회가 전부 성공했다. 중앙값은 loss가 없을 때와 같이 주입 지연만큼 늘었다. p95와 중앙값의 차이는 quic-go 서버에서 지연과 무관하게 약 200 ms(200.1, 199.7, 200.1 ms), quiche에서 약 61 ms(61.3, 61.2, 61.8 ms)였다. aioquic에서는 지연 0 ms일 때 23.9 ms, 지연 50·300 ms일 때 61.3·62.7 ms였다. quic-go 서버의 200 ms는 quic-go client가 RTT sample 없이 쓰는 초기 PTO와 같은 크기이고, 61 ms는 RTT 20 ms에서 계산되는 PTO와 같은 크기다. 손실된 packet과 복구 경로를 handshake별로 대조하지는 않았다. 조건마다 200회이므로 최댓값은 한두 개의 handshake가 정하며 조건 사이의 비교에 쓰지 않는다.

## 7. 가설 판정

| 가설 | 예측 | 판정 |
| --- | --- | --- |
| H1 | 동시 handshake 수를 늘리면 비동기형 hook 구현체의 T_hs 중앙값은 유지되고, 실행 단위를 공유하는 동기형 hook 구현체의 중앙값은 동시 handshake 수에 비례해 증가한다. | 예측대로다. 지연 50 ms에서 동시 1·10·100개의 중앙값은 quiche 71.2·71.6·72.7 ms, aioquic 71.6·277.2·3,228.4 ms였다. 동기형 hook이지만 실행 단위를 공유하지 않는 quic-go는 71.8·72.3·74.4 ms로 유지됐다. |
| H3 전반 | D_sign이 client의 PTO보다 짧으면 T_hs는 지연이 없을 때의 값에 D_sign을 더한 값과 같다. | 예측대로다. |
| H3 후반 | D_sign이 PTO보다 길면 client가 probe packet을 보내고 T_hs의 증가량이 D_sign과 달라진다. | probe packet은 예측대로 나갔다. T_hs의 증가량은 loss가 없을 때 D_sign과 1 ms 안에서 같았으므로 이 부분은 기각한다. |
| H3의 수치 예측 | RTT_c 20 ms에서 ServerHello나 ACK를 먼저 보내는 구현체는 약 60 ms, 그렇지 않은 구현체는 약 1초를 넘는 지점부터 probe가 관측된다. | 먼저 보내는 구현체의 경계는 예측과 같았다(aioquic에서 지연 20 ms는 probe 없음, 50 ms는 전부 probe). 먼저 보내지 않는 quic-go 서버의 경계는 1초가 아니라 200 ms였다. quic-go client의 초기 RTT가 RFC 9002 권고값 333 ms가 아니라 100 ms이기 때문이다. |

H2와 H4는 threshold signer가 필요하므로 이번 측정의 대상이 아니다.

## 8. 한계

- RTT_c는 20 ms 한 값, client 구현체는 quic-go 하나다. 초기 PTO는 client 구현체가 정하므로 probe가 시작되는 지연은 다른 client에서 달라진다.
- 조건마다 50회(loss 조건은 200회)여서 p99를 보고하지 않았다.
- 주입한 지연은 고정값이고 동기형 서버에서는 sleep이다. 실제 threshold signer의 지연은 분포를 가지며 서명 계산과 signer 통신이 CPU를 쓴다.
- quiche 서버는 이 실험용으로 만든 최소 event loop다. 다른 application이 quiche 위에 올린 event loop는 서명 완료를 처리하는 시점이 다를 수 있다.
- host에서 실험과 무관한 프로세스가 CPU를 쓰고 있었다. 측정마다 load average를 기록했으나 그 영향을 분리하지 않았다.
- s2n-quic은 측정하지 않았다.

## 9. 설계에 주는 영향

- 단일 handshake의 T_hs는 $T_{base} + D_{sign}$으로 모델링한다. Phase 1의 남은 측정은 RTT_c 1 ms와 100 ms, 두 번째 client 구현체에서 이 관계가 유지되는지 확인하는 범위로 줄인다.
- 구현체 변인은 T_hs의 덧셈 항이 아니라 처리 용량으로 나타났다. event loop 하나에서 동기 서명을 호출하는 구현체는 서명이 직렬로 처리되므로, 이 구현체의 handshake 처리율 상한은 1/D_sign이다. 종속 변인에 서버가 지속할 수 있는 handshake 처리율을 추가한다.
- 서명 지연의 부수 효과로 client의 첫 RTT sample 왜곡과 probe packet 수를 종속 변인에 추가한다. RTT sample 왜곡이 handshake 이후의 loss recovery에 주는 영향은 측정하지 않았다.
- RQ4의 최적화는 D_sign의 분포를 줄이는 문제로 좁혀진다. D_sign을 정하는 signature scheme, (t, n), signer 배치, signer 선택 정책의 비교는 Phase 2에서 한다.
