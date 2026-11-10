# quic-threshold-handshake

QUIC 서버의 TLS 인증 키를 여러 하위 서버에 나누어 두고 threshold signature로 handshake를 인증할 때, 키 분산 수준과 client가 겪는 handshake 지연시간 사이의 trade-off를 QUIC 구현체, 네트워크 환경, 시스템 환경별로 측정하고 최적 구성을 찾는 연구 저장소다.

> 주제 원문: 다양한 QUIC 구현체와 네트워크 및 시스템 환경에 따른 키 분산 서명 및 하위 서버 간 핸드셰이크 지연시간 trade-off 최적화

## 문제

QUIC handshake에서 서버는 TLS 1.3 CertificateVerify 메시지에 인증서 개인키로 서명한다. 개인키가 QUIC 연결을 종단하는 서버의 메모리에 있으면 그 서버 하나가 침해될 때 키가 유출된다. threshold signature는 키를 n개의 share로 나누어 서로 다른 하위 서버에 두고, t개 이상의 서버가 협력해야 서명이 만들어지게 한다. 어느 서버도 완전한 키를 갖지 않으므로 공격자는 t개 서버를 침해해야 한다.

이 보호에는 지연 비용이 따른다. 서명 하나를 만들려면 handshake 도중에 하위 서버 사이에서 메시지가 한 번 이상 오가야 한다. QUIC handshake는 1-RTT 완료를 전제로 loss recovery timer와 anti-amplification limit을 설계했기 때문에, 서명 지연이 handshake 시간에 그대로 더해지지 않고 재전송이나 대기를 추가로 유발할 수 있다. 서명 요청을 비동기로 처리하는지, 서명을 기다리는 동안 ServerHello나 ACK를 먼저 보내는지는 QUIC 구현체마다 다르다.

## 연구 질문

| 번호 | 질문 |
| --- | --- |
| RQ1 | 어떤 QUIC 구현체가 handshake를 막지 않고 외부 서명 서비스를 호출할 수 있으며, 서명을 기다리는 동안 각 구현체는 어떤 packet을 보내는가? |
| RQ2 | threshold 서명 지연은 signature scheme, (t, n), 하위 서버 간 RTT·loss, CPU 부하에 따라 어떻게 분해되는가? |
| RQ3 | 서명 지연은 client가 관측하는 handshake 시간으로 어떻게 전달되며, 어떤 조건에서 단순 덧셈보다 커지는가? |
| RQ4 | 침해 허용 서버 수와 가용성 제약이 주어졌을 때 handshake 지연시간의 중앙값과 p99를 최소화하는 구성은 무엇인가? |

## 문서

| 문서 | 내용 |
| --- | --- |
| [docs/problem-definition.md](docs/problem-definition.md) | 배경, 시스템 모델, 위협 모델, signature scheme 제약, 연구 질문과 가설, 범위, 미결정 사항 |
| [docs/experiment-design.md](docs/experiment-design.md) | 변인, 테스트베드, 측정 지표, 단계별 실험, 지연 모델과 최적화 문제, 타당성 위협 |
| [docs/related-work.md](docs/related-work.md) | 선행 연구, TLS 라이브러리별 서명 hook 조사표, 참고문헌 |

## 진행 상태

주제 구체화 단계다. 실험 코드와 측정 결과는 아직 없다. 팀이 결정해야 할 항목은 [docs/problem-definition.md](docs/problem-definition.md)의 "미결정 사항"에 정리했다.
