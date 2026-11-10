# 선행 연구와 참고문헌

조사 기준일은 2026년 11월 10일이다. 각 항목은 원문이나 공식 문서에서 확인한 내용만 적었다.

## 1. 키 보호와 원격 서명

Keyless SSL은 CDN edge server가 개인키 없이 TLS handshake를 진행하고 서명 연산만 원격 key server에 맡기는 방식이다. Stebila와 Sullivan은 이 handshake proxying의 보안과 성능을 분석했다[7]. Bhargavan 등은 Keyless SSL의 암호학적 안전성을 분석했다[8]. Delegated Credentials는 인증서 소유자가 유효 기간이 짧은 위임 키를 발급해 edge server가 그 키로 handshake에 서명하게 한다[5].

2023년 NIST MPTS workshop의 Cloudflare 발표는 Keyless 방식과 Threshold TLS를 edge server 침해에 대한 두 가지 대책으로 나란히 놓았다[9]. 발표는 Shoup threshold RSA, FROST, 두 참여자용 threshold ECDSA의 장단점을 비교하고, TLS에 필요한 요구사항으로 빠른 online 서명, online round trip 수 최소화, 키와 메시지에 무관한 precomputation, 명시적인 protocol 기술을 들었다. 같은 발표의 CIRCL prototype 수치는 다음과 같다. threshold RSA-2048(t=2, n=3, safe prime이 아닌 키)에서 signature share 계산이 5 ms, share 결합이 0.16 ms다. FROST(t=3, n=5)에서 signature share 계산이 약 0.3 ms, share 결합이 약 2.3 ms다. 이 수치는 계산 시간이며 signer 사이의 네트워크 지연은 포함하지 않는다.

## 2. threshold signature

Shoup의 threshold RSA는 signer가 다른 signer와 통신하지 않고 signature share를 계산하며, 유효한 share t개를 결합하면 표준 RSA 서명이 된다[11]. FROST는 Schnorr 서명의 threshold 방식으로, 두 round로 쓰거나 preprocessing 단계를 두어 서명 시점에는 한 round로 쓸 수 있다[10]. RFC 9591은 FROST의 두 round protocol을 규정하며, Ed25519와 Ed448 ciphersuite의 서명은 RFC 8032 검증기로 검증된다[6]. RFC 9591의 FROST는 robustness를 제공하지 않으며, ROAST가 이를 보완하는 wrapper protocol이다[14].

ECDSA는 서명식에 비밀 nonce의 역원과 비밀키의 곱이 들어가 threshold 방식이 복잡하다. CGGMP21은 presigning 뒤 non-interactive online 서명, proactive share 갱신, identifiable abort를 제공한다[12]. DKLs23은 oblivious transfer에 기반한 3 round protocol이다[13]. 한 구현 비교 blog는 같은 장비에서 FROST 서명이 수 ms, presigning을 쓰지 않은 CGGMP 서명이 수 초 걸렸다고 보고하고, threshold ECDSA 구현에서 2020년부터 2023년 사이에 보고된 key 추출 취약점을 정리했다[21]. 이 자료는 심사를 거치지 않은 개인 측정이므로 library를 고를 때의 참고 자료로만 쓴다.

## 3. QUIC handshake 성능

Nawrocki 등은 QUIC을 지원하는 서비스 27만 2천 개를 측정해 서버 인증서의 35%가 anti-amplification limit을 넘는 크기여서 1-RTT handshake를 방해한다고 보고했다[15]. Mücke 등은 QUIC Interop Runner로 8개 구현체를 emulation해 instant ACK의 효과를 RTT, loss, 인증서 크기별로 측정했고, instant ACK이 조건에 따라 불필요한 재전송이나 더 긴 대기를 만든다고 보고했다[16]. QUIC Interop Runner는 구현체를 Docker image로 묶어 구현체 쌍마다 test case를 자동으로 실행한다[17].

## 4. handshake 인증 연산의 가속

Looma는 post-quantum 서명의 비용을 줄이기 위해 online/offline signature를 TLS 1.3에 넣어, 비싼 연산을 handshake 경로 밖에서 미리 수행한다. picotls를 확장해 구현했고, Dilithium-2를 쓰는 handshake보다 지연시간을 최대 44% 줄였다고 보고했다[18]. Looma의 대상은 datacenter 안의 TLS 연결이다. precomputation으로 handshake 경로의 서명 비용을 줄인다는 발상은 threshold signature의 nonce commitment·presignature precomputation과 같은 방향이다.

## 5. TLS 라이브러리의 서명 hook 조사표

QUIC 구현체는 TLS 라이브러리를 통해 CertificateVerify 서명을 만든다. 아래 표의 "hook"과 "비동기 지원"은 각 라이브러리의 header·source·공식 문서에서 확인했다. "QUIC 구현체" 열은 각 구현체의 일반적인 구성이며, QUIC 계층이 서명 미완료 상태를 받아 handshake를 재개하는지와 대기 중 어떤 packet을 보내는지는 Phase 0에서 확인한다.

| TLS 라이브러리 | 서명 hook | 비동기 지원 | QUIC 구현체 | 근거 |
| --- | --- | --- | --- | --- |
| BoringSSL | `SSL_PRIVATE_KEY_METHOD`의 `sign`과 `complete` | 지원한다. `sign`이 `ssl_private_key_retry`를 반환하면 나중에 `complete`로 재개한다. | Cloudflare quiche, lsquic, Chrome | [22] |
| picotls | `sign_certificate` callback | 지원한다. `ptls_handshake`가 `PTLS_ERROR_ASYNC_OPERATION`을 반환하고 `ptls_async_job_t`로 완료를 알린다. | picoquic, quicly | [23] |
| s2n-tls | `s2n_config_set_async_pkey_callback` | 지원한다. callback이 연산을 끝내지 않고 반환하면 `s2n_negotiate`가 blocked 상태를 반환하고, `s2n_async_pkey_op_apply` 호출 뒤 재개한다. 외부에서 계산한 결과를 넣는 `s2n_async_pkey_op_set_output`도 있다. | s2n-quic | [24] |
| Go `crypto/tls` | `tls.Certificate.PrivateKey`에 넣는 `crypto.Signer` | `Sign` 호출이 서명을 반환할 때까지 끝나지 않는 동기 interface다. | quic-go, go-x-net | [25] |
| rustls 0.23 | `sign::SigningKey`와 `sign::Signer::sign` | `sign`이 서명을 반환하는 동기 trait이다. | quinn | [26] |

mvfst(fizz), msquic, ngtcp2, nginx, aioquic, neqo, xquic의 서명 hook은 아직 확인하지 않았다.

## 6. 이 연구가 채우려는 공백

위 연구들은 threshold signature의 계산 비용[9][21], 원격 서명의 보안과 성능[7][8], QUIC handshake와 인증서 크기·ACK 전략의 상호작용[15][16]을 각각 다뤘다. threshold signing의 지연을 QUIC handshake의 loss recovery, anti-amplification limit과 함께 여러 구현체에서 측정한 연구는 조사 기준일의 검색에서 찾지 못했다. 이 판단은 검색 범위에 한정되므로 Phase 0에서 다시 확인한다.

## 참고문헌

1. J. Iyengar, M. Thomson (eds.), "QUIC: A UDP-Based Multiplexed and Secure Transport," RFC 9000, 2021. https://www.rfc-editor.org/rfc/rfc9000
2. M. Thomson, S. Turner (eds.), "Using TLS to Secure QUIC," RFC 9001, 2021. https://www.rfc-editor.org/rfc/rfc9001
3. J. Iyengar, I. Swett (eds.), "QUIC Loss Detection and Congestion Control," RFC 9002, 2021. https://www.rfc-editor.org/rfc/rfc9002
4. E. Rescorla, "The Transport Layer Security (TLS) Protocol Version 1.3," RFC 8446, 2018. https://www.rfc-editor.org/rfc/rfc8446
5. R. Barnes, S. Iyengar, N. Sullivan, E. Rescorla, "Delegated Credentials for TLS and DTLS," RFC 9345, 2023. https://www.rfc-editor.org/rfc/rfc9345
6. D. Connolly, C. Komlo, I. Goldberg, C. A. Wood, "The Flexible Round-Optimized Schnorr Threshold (FROST) Protocol for Two-Round Schnorr Signatures," RFC 9591, 2024. https://www.rfc-editor.org/rfc/rfc9591
7. D. Stebila, N. Sullivan, "An Analysis of TLS Handshake Proxying," IEEE TrustCom 2015. https://doi.org/10.1109/Trustcom.2015.385
8. K. Bhargavan, I. Boureanu, P.-A. Fouque, C. Onete, B. Richard, "Content Delivery over TLS: A Cryptographic Analysis of Keyless SSL," IEEE EuroS&P 2017.
9. A. Faz-Hernández, "Requirements for Threshold TLS," NIST Workshop on Multi-Party Threshold Schemes (MPTS) 2023, 발표 자료. https://csrc.nist.gov/csrc/media/Presentations/2023/mpts2023-day2-talk-threshold-tls-rsa/images-media/mpts2023-2b1-slides--armando--threshold-TLS.pdf
10. C. Komlo, I. Goldberg, "FROST: Flexible Round-Optimized Schnorr Threshold Signatures," Selected Areas in Cryptography (SAC) 2020, LNCS 12804, pp. 34-65. https://doi.org/10.1007/978-3-030-81652-0_2
11. V. Shoup, "Practical Threshold Signatures," EUROCRYPT 2000. https://www.iacr.org/archive/eurocrypt2000/1807/18070209-new.pdf
12. R. Canetti, R. Gennaro, S. Goldfeder, N. Makriyannis, U. Peled, "UC Non-Interactive, Proactive, Threshold ECDSA with Identifiable Aborts," ACM CCS 2020. https://eprint.iacr.org/2021/060
13. J. Doerner, Y. Kondi, E. Lee, a. shelat, "Threshold ECDSA in Three Rounds," IEEE S&P 2024. https://eprint.iacr.org/2023/765
14. T. Ruffing, V. Ronge, E. Jin, J. Schneider-Bensch, D. Schröder, "ROAST: Robust Asynchronous Schnorr Threshold Signatures," ACM CCS 2022.
15. M. Nawrocki, P. F. Tehrani, R. Hiesgen, J. Mücke, T. C. Schmidt, M. Wählisch, "On the Interplay between TLS Certificates and QUIC Performance," ACM CoNEXT 2022. https://doi.org/10.1145/3555050.3569123
16. J. Mücke, M. Nawrocki, R. Hiesgen, T. C. Schmidt, M. Wählisch, "ReACKed QUICer: Measuring the Performance of Instant Acknowledgments in QUIC Handshakes," ACM IMC 2024. https://doi.org/10.1145/3646547.3689022
17. M. Seemann, J. Iyengar, "Automating QUIC Interoperability Testing," ACM EPIQ 2020. https://doi.org/10.1145/3405796.3405826 (도구: https://github.com/quic-interop/quic-interop-runner)
18. X. Ma, M. Honda, "Looma: A Low-Latency PQTLS Authentication Architecture for Cloud Applications," NDSS 2026. https://dx.doi.org/10.14722/ndss.2026.240074
19. CA/Browser Forum, "Baseline Requirements for the Issuance and Management of Publicly-Trusted TLS Server Certificates," version 2.2.1, 6.1.5절. https://cabforum.org/working-groups/server/baseline-requirements/documents/CA-Browser-Forum-TLS-BR-2.2.1.pdf
20. "Efficient Threshold ML-DSA," IACR ePrint 2026/013. https://eprint.iacr.org/2026/013
21. 808bits, "The ECDSA tax: GG20, CGGMP21, DKLs23 and FROST compared," 2026, 비심사 blog. https://808bits.com/articles/the-ecdsa-tax/
22. BoringSSL, `include/openssl/ssl.h`의 `ssl_private_key_method_st`. https://github.com/google/boringssl/blob/main/include/openssl/ssl.h
23. picotls, `include/picotls.h`의 `ptls_async_job_t`와 `sign_certificate`. https://github.com/h2o/picotls/blob/master/include/picotls.h
24. s2n-tls Usage Guide, "Offloading Private Key Operations." https://aws.github.io/s2n-tls/usage-guide/ch13-private-key-ops.html
25. Go `crypto/tls`, `common.go`의 `Certificate.PrivateKey`. https://github.com/golang/go/blob/master/src/crypto/tls/common.go
26. rustls 0.23.31, `rustls/src/crypto/signer.rs`. https://github.com/rustls/rustls/blob/v/0.23.31/rustls/src/crypto/signer.rs
