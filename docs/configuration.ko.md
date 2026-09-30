# 설정 레퍼런스

[README](../README.ko.md) · [Helm 설정](../charts/fortigate-external-dns/README.md)

설정은 플래그 또는 환경 변수로 제공할 수 있습니다. FortiGate 자격 증명은 Kubernetes Secret에서 가져와야 합니다. FortiGate 기본 URL에는 userinfo, query parameter, fragment를 넣을 수 없으며 API 인증은 토큰 설정으로만 받습니다.

자주 쓰는 플래그:

```sh
fortigate-external-dns \
  --provider=fortigate \
  --source=service \
  --source=ingress \
  --source=gateway \
  --domain-filter=example.com \
  --owner-id=my-cluster \
  --fortigate-url=https://fortigate.example.com \
  --fortigate-zone=example.com \
  --fortigate-exclusive-zone-ownership \
  --dry-run \
  --fortigate-vdom=root
```

필수 Secret 값:

```sh
FORTIGATE_API_TOKEN=<api-token-from-kubernetes-secret>
```

컨트롤러는 FortiGate가 아닌 프로바이더를 거부합니다.

환경 변수는 엄격하게 파싱됩니다. 비어 있지 않은데 파싱할 수 없는 값(예: `DRY_RUN=ture`,
단위 없는 `INTERVAL=30`)은 조용히 기본값으로 폴백하지 않고 **시작을 실패**시킵니다.
이로써 오타가 난 `DRY_RUN`이 쓰기를 몰래 활성화하는 것을 방지합니다.

## 운영성 플래그

| 플래그 | 환경 변수 | 기본값 | 용도 |
| --- | --- | --- | --- |
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | 전용 database의 stale 레코드 처리 방식: `delete`(파괴적 삭제), `deactivate`(비활성화 후 유지), `keep`(삭제하지 않음). source 또는 namespace 범위를 제한하면 `keep`이 필수입니다. |
| `--allow-empty-desired-cleanup` | `ALLOW_EMPTY_DESIRED_CLEANUP` | `false` | 대량 정리(mass-cleanup) 가드 해제. 기본적으로 디스커버리가 *성공*했는데 원하는 엔드포인트가 0개인 사이클은 모든 정리 작업을 거부합니다 — 이는 해체가 아니라 설정 실수(`--domain-filter`/`--namespace` 오설정)의 신호이기 때문입니다. 의도적인 해체(decommissioning) 시에만 켜세요. |
| `--max-cleanup-per-cycle` | `MAX_CLEANUP_PER_CYCLE` | `0` | 한 사이클에 계획된 delete/deactivate 작업이 이 수를 넘으면 해당 사이클의 정리를 거부합니다(`0` = 무제한). 생성/갱신은 그대로 적용되고, 거부는 error 로그와 `cleanup_refused_total` 메트릭으로 드러납니다. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Kubernetes list 및 FortiGate 호출을 포함해 각 재조정 루프에 시간 상한을 둡니다. |
| `--interval` | `INTERVAL` | `1m` | 재조정 루프 사이의 간격입니다. |
| `--default-ttl` | `DEFAULT_TTL` | `300` | source가 TTL을 지정하지 않을 때 쓰는 기본 DNS 레코드 TTL(초)입니다. |
| `--fortigate-timeout` | `FORTIGATE_TIMEOUT` | `15s` | FortiGate API 요청별 타임아웃입니다. |
| `--fortigate-retries` | `FORTIGATE_RETRIES` | `2` | 재시도 가능한 FortiGate API 실패의 재시도 횟수입니다. |
| `--leader-election` | `LEADER_ELECTION` | `true` | 다중 레플리카 배포를 위한 Lease 기반 단일 쓰기 가드. `--once`에서는 무시됩니다. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease 이름. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | 파드 네임스페이스 | Lease가 위치할 네임스페이스. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | `/healthz`, `/readyz`, `/metrics`의 바인드 주소. 비우면 서버가 비활성화됩니다(프로브도 함께 꺼짐). |
| `--healthz-max-staleness` | `HEALTHZ_MAX_STALENESS` | `0` (자동) | liveness 하트비트 윈도우: 이 레플리카가 재조정을 담당하는 동안(리더이거나 리더 선출 비활성) 윈도우 내에 재조정 시도가 하나도 *완료*되지 않으면 `/healthz`가 실패해 멈춘(wedged) 루프를 재시작합니다. 실패한 시도도 완료로 칩니다 — FortiGate 장애만으로는 파드가 재시작되지 않습니다. `0`이면 `max(5×interval, 5m)`, 타깃 모드에서는 `max(5×max(interval, resync), 5m)`을 사용합니다. |
| `--fortigate-ca-file` | `FORTIGATE_CA_FILE` | (없음) | FortiGate TLS 인증서 검증에 시스템 루트 *대신* 사용할 PEM CA 번들 경로 — 사설 CA 장비를 신뢰하는 올바른 방법입니다. `--fortigate-insecure-skip-verify`와 상호 배타적이며(둘 다 설정하면 검증 실패) 어느 쪽이든 TLS 1.2가 최저 버전으로 강제됩니다. |
| `--fortigate-exclusive-zone-ownership` | `FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP` | `false` | 쓰기 전 필수 확인. 설정된 FortiGate DNS database의 모든 레코드를 이 컨트롤러만 관리함을 확인합니다. 공유/수동 레코드는 지원하지 않으며 source 또는 namespace 범위를 제한하면 `cleanup-policy=keep`이 필요합니다. |
| `--log-format` | `LOG_FORMAT` | `text` | 로그 출력 형식: `text` 또는 `json`(로그 수집 파이프라인용). |
| `--log-level` | `LOG_LEVEL` | `info` | 로그 레벨: `debug`, `info`, `warn`, `error`. |
| `--version` | — | — | 스탬프된 버전과 커밋을 출력하고 종료합니다. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (없음) | 부모 Gateway 주소 해석에만 참조하는 추가 네임스페이스. 조회 범위 전용이며 소유권/정리(cleanup) 범위를 넓히지 않습니다. 네임스페이스 한정 설치 시 Helm 차트가 이 네임스페이스마다 읽기 전용 `gateways` Role을 자동 생성합니다. |
| `--plan-output` | `PLAN_OUTPUT` | (없음) | `--once`와 함께 자격 증명이 없는 canonical 재조정 plan을 원자적으로 파일에 기록합니다. 기존 파일은 명시적 덮어쓰기 없이는 거부합니다. |
| `--plan-output-overwrite` | `PLAN_OUTPUT_OVERWRITE` | `false` | `--once --plan-output`에서 기존 plan 파일 교체를 명시적으로 허용합니다. |
| `--approved-plan-hash` | `APPROVED_PLAN_HASH` | (없음) | `--once`에서 새로 생성된 canonical plan의 소문자 SHA-256과 정확히 일치할 때만 적용하며 provider, source, policy, ownership 상태를 적용 직전에 다시 구성해 재검증합니다. |
| `--target-mode` | `TARGET_MODE` | `false` | 직접 FortiGate 플래그 대신 namespaced `FortiGateDNSTarget` 리소스를 사용합니다. 두 모드는 상호 배타적입니다. |
| `--platform-namespace` | `PLATFORM_NAMESPACE` | pod namespace | 타깃, claim, plan, status 리소스가 있는 namespace입니다. `FortiGateDNSPolicy`는 platform namespace가 아니라 *source* namespace(`--namespace`, 미설정 시 모든 namespace)에서 읽습니다. |
| `--policy-enforcement` | `POLICY_ENFORCEMENT` | `false` | plan 전에 일치하는 `FortiGateDNSPolicy`를 평가합니다. |
| `--event-driven` | `EVENT_DRIVEN` | `false` | target-mode informer/workqueue 재조정을 켭니다. 주기적 `--resync`는 전체 audit 및 credential rotation 경계로 유지됩니다. |
| `--debounce` / `--resync` | `DEBOUNCE` / `RESYNC` | `2s` / `1m` | semantic event 병합과 주기적 전체 audit을 제한합니다. |
| `--status-retention` | `STATUS_RETENTION` | `20` | 타깃별 status/audit 이력을 1~100개 유지합니다. |
| `--plan-retention` | `PLAN_RETENTION` | `20` | 완료된 change plan을 1~100개 유지합니다. status/audit 보존 수와 독립적입니다. |
| `--publish-external-name-services` | `PUBLISH_EXTERNAL_NAME_SERVICES` | `false` | 타깃/정책이 허용한 ExternalName CNAME 게시를 허용합니다. |
| `--publish-headless-services` | `PUBLISH_HEADLESS_SERVICES` | `false` | opt-in headless Service의 EndpointSlice A/AAAA 게시를 허용합니다. |

메트릭은 `fortigate_external_dns_` 접두사로 Prometheus 텍스트 형식으로 노출됩니다
(재조정 카운터, 재조정 소요 시간 히스토그램, type/result 라벨이 붙은 작업 카운터 —
`planned`, `applied`, `failed`, `skipped`, `conflict` — 마지막 성공 재조정
타임스탬프, 대량 정리 가드 발동을 세는 `cleanup_refused_total` 카운터, 버전/커밋을
담은 `build_info` 게이지). 토큰이나 레코드 페이로드는 노출하지 않습니다.

Target mode는 타깃 health, queue depth, 정책 거부, 소유권/adoption, plan phase,
audit 상태용 플랫폼 메트릭을 채웁니다. 메트릭에는 자격 증명이 없으며 타깃 장애는
독립적으로 보고됩니다.
