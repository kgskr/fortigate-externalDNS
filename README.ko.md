# FortiGate ExternalDNS

📖 [English README](README.md)

FortiGate ExternalDNS는 ExternalDNS의 재조정(reconciliation) 모델에서 영감을 받은, FortiGate 전용 Kubernetes 컨트롤러입니다. 지원하는 Kubernetes 네트워킹 리소스에서 DNS 의도를 발견하고, 그 결과 DNS 레코드를 FortiGate API를 통해 FortiGate 장비에 반영합니다.

이 프로젝트는 의도적으로 FortiGate 전용입니다. Route53, Google Cloud DNS, Cloudflare, webhook 프로바이더, 서비스 메시 API, 임의의 서드파티 CRD는 지원하지 않습니다.

## 아키텍처와 기능 상태

런타임은 직접 단일 타깃 모드와 CRD 기반 멀티 타깃 모드를 모두 지원합니다. 소스
discovery로 desired 레코드를 만들고, revision이 안정적인 FortiGate snapshot과
비교해 안전 가드가 plan을 만든 뒤 로그 출력, exact-hash 승인을 위한 저장, 또는
적용을 수행합니다. 리더 선출은 writer를 하나로 유지하고 health/metrics 서버는
타깃별 재조정 상태를 제공합니다.

`platform.targetMode.enabled=true`이면 namespaced `FortiGateDNSTarget` 리소스를
활성화합니다. 각 타깃은 API 토큰과 선택적 CA를 메모리에서 해석하고 독립적으로
실행되며, resync 때 회전된 값을 다시 읽습니다. 공유 소유권, 정책 적용, CRD
exact-hash 승인, status/audit 이력, ExternalName, headless EndpointSlice 게시를
선택적으로 활성화할 수 있습니다. 모든 플랫폼 기능은 기본 비활성화입니다.

| 기능 | 현재 상태 | 안전 기본값 / 활성화 게이트 |
| --- | --- | --- |
| 단일 FortiGate 타깃과 전용 database | 지원 | Helm은 `dryRun: true`로 시작하며 쓰기에는 전용 소유권 확인이 필요합니다. |
| Canonical 일회성 plan과 정확한 hash 승인 | `--once`에서 지원 | 적용 직전 provider 상태를 다시 조회하고 전제조건 변경을 거부합니다. |
| 플랫폼 CRD 5개, 최소 권한 RBAC, dashboard/alert | 지원 | 기본 비활성화이며 필요한 차트 기능만 켭니다. |
| 멀티 타깃, 공유 claim, 정책, 이벤트 큐, 상태 이력 | target mode에서 지원 | 타깃 장애와 쓰기/승인은 타깃별로 격리됩니다. adoption과 target/type 교체는 운영자 관리 절차입니다. |
| ExternalName 및 headless dual-stack EndpointSlice 확장 | 지원 | 기본 비활성화이며 global/chart flag와 타깃 및 오브젝트/정책 opt-in이 필요합니다. |
| 이미지/차트 서명, SBOM, provenance 검증 | 게시 릴리스에서 지원 | immutable digest와 정확한 release workflow identity를 검증합니다. |

## 지원 소스

- Kubernetes `Service`
- Kubernetes `Ingress`
- Kubernetes SIG Gateway API `Gateway`
- Kubernetes SIG Gateway API `HTTPRoute`

Gateway API는 CRD로 설치되지만 표준 Kubernetes 네트워킹 API로 취급해 지원합니다. 그 외 CRD는 호스트네임 유사 필드를 스캔하지 않습니다.

## DNS 범위

직접 쓰기 모드는 **컨트롤러 전용 FortiGate DNS database**를 지원합니다. Target
mode에서는 `exclusive`와 claim-gated `shared` 소유권을 모두 지원합니다. 공유
모드의 create/update/delete에는 정확한 소유권 전제조건이 필요하고 파괴적 변경에는
현재 `Confirmed` claim이 필요합니다. 기존 미소유 레코드를 암묵적으로 adoption하지
않습니다. 현재 runtime은 승인 계약으로 claim identity 변경을 표현할 수 없어
adoption과 공유 레코드의 target/type 변경을 거부합니다. 소유권을 문서화되지 않은 FortiGate 필드에
저장하지 않습니다.

지원하는 레코드 타입은 타깃 값에서 유도됩니다:

- IPv4 타깃 -> `A`
- IPv6 타깃 -> `AAAA`
- DNS 이름 타깃 -> `CNAME`

### 재조정 안전성

- FortiGate 목록 페이지네이션이 동일 revision으로 완전하게 끝나야 계획을 세우며,
  잘렸거나 조회 중 변경된 snapshot은 fail-closed 처리합니다.
- 모든 desired 타깃이 관측될 때까지 교체 정리를 미루고, 생성 실패 뒤에는 마지막
  정상 타깃을 지우는 의존 cleanup을 실행하지 않습니다.
- Gateway API가 설치되어 있고 HTTPRoute 목록이 단순히 비어 있는 경우에도 Gateway
  listener 레코드는 desired 상태에 남습니다. Gateway API 리소스 자체가 없을 때만
  Gateway discovery를 건너뜁니다.
- HTTPRoute 타깃은 route의 현재 generation에 대해 `Accepted=True` 및
  `ResolvedRefs=True` 조건을 가진 부모 Gateway 참조에서만 게시됩니다.
- FortiGate API 토큰은 `FORTIGATE_API_TOKEN` 또는 `--fortigate-api-token`으로
  제공할 수 있으며, 생성된 help/default 텍스트에는 토큰 값이 노출되지 않습니다.

## FortiOS 호환성

컨트롤러는 안정적인 CMDB REST API
(`/api/v2/cmdb/system/dns-database/{zone}/dns-entry`)와 `Authorization: Bearer`
토큰 인증만 사용합니다. 읽고 쓰는 필드(`hostname`, `type`, `ip`, `ipv6`,
`canonical-name`, `ttl`, `status`)와 정수 레코드 키
(`q_origin_key`/`id`)는 아래 릴리스 전반에서 일관됩니다.

| FortiOS | 상태 | 비고 |
| --- | --- | --- |
| 7.0 / 7.2 / 7.4 / 7.6 | ✅ 지원 | CMDB `system/dns-database` API와 Bearer 토큰 인증이 이 릴리스 전반에서 안정적입니다. |
| 8.0 | ✅ 지원 | API 토큰은 **HTTPS 필수** — 평문 `http://`는 장비가 거부합니다. `https://` URL을 사용하세요(기본값). |
| 6.4 이하 | ⚠️ 미검증 | CMDB API와 Bearer 헤더는 6.0+부터 존재하지만, 이 릴리스들은 여기서 검증하지 않았습니다. |
| 5.6 이하 | ❌ 미지원 | 이 컨트롤러가 사용하는 Bearer 토큰 API 모델 이전 버전입니다. |

참고:

- 대상 zone은 FortiGate에 `config system dns-database` 항목으로 **미리 존재**해야 합니다(보통 primary/`master` zone). 컨트롤러는 zone 자체를 생성하지 않으며, 쓰기 모드에서는 해당 database 전체가 이 컨트롤러 전용이어야 합니다.
- 컨트롤러는 지원하는 모든 FortiOS 타깃에 `https://`를 필수로 요구하고, 인증 요청을 전달하기 전에 모든 API 리디렉션을 거부합니다. 사설 CA 인증서를 쓰는 장비라면 `--fortigate-insecure-skip-verify`로 검증을 끄는 대신 `--fortigate-ca-file`(차트에서는 `fortigate.caBundle`)로 발급 체인을 지정하세요. 두 옵션은 상호 배타적이며, 모두 HTTPS 강제와는 별개입니다.
- 호환성은 Fortinet 공식 문서를 기준으로 검증했습니다. 특정 펌웨어에서 프로덕션 배포 전에 대상 장비를 상대로 `--dry-run --once`를 한 번 돌려보세요 — 컨트롤러가 FortiGate 응답 envelope를 검증하여 스키마/API 불일치를 안전하게 드러냅니다.

## 설정

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

### 운영성 플래그

| 플래그 | 환경 변수 | 기본값 | 용도 |
| --- | --- | --- | --- |
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | 전용 database의 stale 레코드 처리 방식: `delete`(파괴적 삭제), `deactivate`(비활성화 후 유지), `keep`(삭제하지 않음). source 또는 namespace 범위를 제한하면 `keep`이 필수입니다. |
| `--allow-empty-desired-cleanup` | `ALLOW_EMPTY_DESIRED_CLEANUP` | `false` | 대량 정리(mass-cleanup) 가드 해제. 기본적으로 디스커버리가 *성공*했는데 원하는 엔드포인트가 0개인 사이클은 모든 정리 작업을 거부합니다 — 이는 해체가 아니라 설정 실수(`--domain-filter`/`--namespace` 오설정)의 신호이기 때문입니다. 의도적인 해체(decommissioning) 시에만 켜세요. |
| `--max-cleanup-per-cycle` | `MAX_CLEANUP_PER_CYCLE` | `0` | 한 사이클에 계획된 delete/deactivate 작업이 이 수를 넘으면 해당 사이클의 정리를 거부합니다(`0` = 무제한). 생성/갱신은 그대로 적용되고, 거부는 error 로그와 `cleanup_refused_total` 메트릭으로 드러납니다. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Kubernetes list 및 FortiGate 호출을 포함해 각 재조정 루프에 시간 상한을 둡니다. |
| `--leader-election` | `LEADER_ELECTION` | `true` | 다중 레플리카 배포를 위한 Lease 기반 단일 쓰기 가드. `--once`에서는 무시됩니다. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease 이름. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | 파드 네임스페이스 | Lease가 위치할 네임스페이스. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | `/healthz`, `/readyz`, `/metrics`의 바인드 주소. 비우면 서버가 비활성화됩니다(프로브도 함께 꺼짐). |
| `--healthz-max-staleness` | `HEALTHZ_MAX_STALENESS` | `0` (자동) | liveness 하트비트 윈도우: 이 레플리카가 재조정을 담당하는 동안(리더이거나 리더 선출 비활성) 윈도우 내에 재조정 시도가 하나도 *완료*되지 않으면 `/healthz`가 실패해 멈춘(wedged) 루프를 재시작합니다. 실패한 시도도 완료로 칩니다 — FortiGate 장애만으로는 파드가 재시작되지 않습니다. `0`이면 `max(5×interval, 5m)`을 사용합니다. |
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
| `--platform-namespace` | `PLATFORM_NAMESPACE` | pod namespace | 타깃, 정책, claim, plan, status 리소스가 있는 namespace입니다. |
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

타깃 health, queue depth, 정책 거부, 소유권/adoption, plan phase, audit 상태용
Target mode는 타깃 health, queue depth, 정책 거부, 소유권/adoption, plan phase,
audit 상태용 플랫폼 메트릭을 채웁니다. 메트릭에는 자격 증명이 없으며 타깃 장애는
독립적으로 보고됩니다.

### 안전 불변조건

- cleanup 또는 승인을 수행하려면 완전하고 안정적인 provider revision이 필요합니다.
- 한 source 객체의 hostname/target 곱이 1,024개를 넘거나 한 reconcile의 합계가
  10,000개를 넘으면 endpoint 할당 전에 해당 객체 전체를 거부합니다. source를
  incomplete로 표시하므로 cleanup도 중단됩니다.
- dry-run은 FortiGate를 변경하거나 소유권 confirmation을 꾸며내지 않습니다.
- 공유 변경에는 정확히 `Confirmed`인 claim이 필요하며 CRD 손실은 provider 삭제
  권한을 뜻하지 않습니다.
- 현재 runtime은 공유 adoption과 target/type replacement를 거부합니다. 컨트롤러
  쓰기를 멈추고 claim/finalizer를 보존한 상태에서 감사 가능한 운영 절차를 사용해야
  합니다. source UID를 만들어내거나 `status.phase=Confirmed`를 직접 쓰면 안 됩니다.
- discovery, 정책, 소유권, 타깃, provider 상태가 바뀐 승인은 재사용할 수 없습니다.
- 쓰기 타깃 범위가 겹치면, 양쪽 모두 `cleanupPolicy=keep`이고 overlap을 명시적으로
  허용한 비파괴 모드가 아닌 한 잘못된 설정입니다.

### 클러스터 레코드 해체(decommissioning)

전용 database를 의도적으로 비우려면(예: 클러스터 폐기) 완전하고 제한되지 않은
discovery와 empty-desired 가드를 사용해 마지막 한 사이클을 실행해야 합니다:

```sh
fortigate-external-dns --once --allow-empty-desired-cleanup \
  --source=service --source=ingress --source=gateway \
  --fortigate-exclusive-zone-ownership \
  --cleanup-policy=delete ... # 나머지 FortiGate 플래그
```

해제하지 않으면 소유 레코드 전체를 삭제하게 될 사이클은 거부되고
`cleanup_refused_total{reason="empty-desired"}`로 보고됩니다.

## 마이그레이션 및 운영 런북

> **안전 게이트:** 플랫폼 기능은 기본 비활성화입니다. 아래 backup, overlap, 정책,
> claim, 승인, rollback 검사를 통과할 때까지 새 타깃을 `cleanupPolicy=keep`
> dry-run으로 유지하세요. 전용 타깃마다 Deployment 하나를 두는 방식도 계속
> 지원되는 격리 대안입니다.

### 전용 소유권에서 공유 소유권으로

1. 새 타깃을 `cleanupPolicy=keep` dry-run으로 유지하고 이전 컨트롤러 변경을 멈춘
   뒤 FortiGate DNS database를 별도 수단으로 백업합니다.
2. Secret 내용 없이 Kubernetes 메타데이터를 백업합니다:
   `kubectl get fortigatednstargets,fortigatednsrecordownerships,fortigatednschangeplans,fortigatednsstatuses -A -o yaml > platform-backup.yaml`.
3. 모든 provider row를 검토합니다. 현재 runtime은 기존 미소유 row를 adoption하지
   않으므로 공유 쓰기를 멈춘 채 감사 가능한 운영 절차로 해당 row를 마이그레이션합니다.
4. 마이그레이션 동안 claim/finalizer를 보존합니다. source UID를 만들어내거나 claim
   status를 직접 patch하지 않습니다. 새 claim은 현재 관측한 Kubernetes 객체의 실제
   API version과 UID로 예약되어야 합니다.
5. 변경 가능한 모든 레코드에 confirmed claim이 있고 최신 dry-run에 conflict가
   없을 때만 쓰기를 켭니다.
   target 또는 record type replacement는 runtime에서 지원하지 않으므로 쓰기를
   멈추고 위 운영 절차를 사용합니다. 이전 claim은 새 record identity를 승인하지 않습니다.
6. 공유 database에 이전 전용 컨트롤러를 절대 함께 실행하지 않습니다. rollback은
   먼저 쓰기를 끄고 claim/finalizer를 보존한 뒤 FortiGate를 확인하고, 공유
   컨트롤러를 멈춘 후에만 이전 전용 database/controller를 복원합니다.

`samples/`의 adoption/approval CR은 검토용 형태일 뿐입니다. 실제 fingerprint,
revision, canonical document와 hash는 컨트롤러가 생성해야 합니다.

### Legacy에서 멀티 타깃으로

기존 Deployment마다 dry-run `FortiGateDNSTarget` 하나를 만들고 Secret/CA key
reference만 사용합니다. 기존 source, namespace, domain, VDOM, zone, cleanup,
controller identity 경계를 보존합니다. 쓰기 DNS 범위가 겹치지 않는지 검증하세요.
dry-run 타깃은 writer가 아니지만, 의도적인 비파괴 overlap은 양쪽 모두
`cleanupPolicy=keep`과 `allowNonDestructiveOverlap=true`가 필요합니다. 타깃을
독립적으로 검토하고 하나씩 활성화해 한 타깃의 인증/TLS/API/정책 실패가 다른
타깃의 변경 권한으로 이어지지 않게 합니다.

토큰과 CA 오브젝트는 타깃별로 하나씩 회전하고 건강 상태를 확인한 뒤 이전 값을
폐기합니다. Target mode는 credential을 메모리에만 보관하고 resync 때 reference를
다시 읽으며 영향받은 타깃 client만 재구성하므로 pod restart가 필요하지 않습니다.
직접 단일 타깃 차트 경로는 Secret 회전 후 계속
`kubectl rollout restart deployment/<name>`이 필요합니다(인라인
`fortigate.caBundle` 변경은 자동 rollout). 타깃별 Deployment, ServiceAccount,
credential Secret, 전용 database 방식도 지원되는 운영 대안입니다.

### 해체와 재해 복구

전용 타깃은 위의 guarded final cycle을 실행하고 FortiGate 상태를 검증한 뒤
제거합니다. 공유 모드에서는 먼저 쓰기를 멈추고 desired source를 제거하세요.
provider 레코드를 의도대로 유지/삭제하고 부재를 확인하기 전에 claim/plan/target
CRD나 finalizer를 삭제하면 안 됩니다.

플랫폼 CRD가 손실되면 모든 writer를 중지합니다. claim 부재를 삭제 권한으로
해석하거나 `Confirmed` 상태를 손으로 만들지 마세요. API와 알려진 정상 메타데이터
백업을 복원하고 FortiGate snapshot을 새로 받은 뒤 정확한 provider ID/fingerprint를
런타임이 다시 검증하게 합니다. 불확실한 row는 검토 전까지 orphan/conflict로
남습니다. status와 완료 plan 이력은 1~100개(차트 기본 20)로 제한되며 pending,
approved, applying, interrupted plan은 완료 audit 이력처럼 정리하지 않습니다.

### 문제 해결

| 증상 | 확인 / 대응 |
| --- | --- |
| dry-run에 예상 밖 대량 cleanup | source API, `domainFilters`, namespace, zone을 확인하고 empty-desired override를 끈 채 유지합니다. |
| 승인 hash 거부 | plan을 다시 생성합니다. canonical bytes 또는 전제조건이 바뀌었으며 64자리 소문자 SHA-256만 허용됩니다. |
| 타깃/정책/claim CR이 있지만 아무 동작 없음 | `platform.targetMode.enabled`, namespace/RBAC, target status condition, policy selector, exact plan 승인을 확인합니다. |
| 공유 claim이 `Confirmed`가 아님 | 쓰기를 켜지 말고 conflict, provider revision, ID/fingerprint, 승인 상태를 확인합니다. |
| 타깃 범위가 서로 겹침 | zone/domain을 분리하거나 양쪽을 명시적인 비파괴 모드로 유지합니다. |
| 토큰/CA 회전 후 인증/TLS 실패 | 이전 참조 오브젝트를 복원하고 해당 타깃을 격리한 뒤 검증 후 다시 회전합니다. |
| CRD/claim이 사라짐 | writer를 멈추고 재해 복구 절차를 따르며 부재에서 provider 소유권을 추정하지 않습니다. |

## 로컬 Dry Run

쓰기를 허용하기 전에 dry-run 모드를 사용하세요:

```sh
FORTIGATE_API_TOKEN=placeholder \
go run ./cmd/fortigate-external-dns \
  --once \
  --dry-run \
  --kubeconfig "$HOME/.kube/config" \
  --source=service \
  --source=ingress \
  --source=gateway \
  --domain-filter=example.com \
  --owner-id=my-cluster \
  --fortigate-url=https://fortigate.example.com \
  --fortigate-zone=example.com
```

## Helm 설치

릴리스된 차트 버전은 GHCR에 OCI 아티팩트로 게시됩니다:

```sh
helm show chart oci://ghcr.io/kgskr/charts/fortigate-external-dns --version 0.3.0
```

먼저 Secret을 만듭니다:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token='<fortigate-api-token>'
```

게시된 차트로 설치합니다:

```sh
helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.3.0 \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com'
```

소스 체크아웃에서 바로 설치하려면 다음처럼 사용합니다:

```sh
git clone https://github.com/kgskr/fortigate-externalDNS.git
cd fortigate-externalDNS

helm install fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com'
```

> **차트 기본값은 `dryRun: true`입니다**: 컨트롤러는 레코드를 발견하고 계획을
> 로그로 남기지만 FortiGate에는 **아무것도 쓰지 않습니다**. 먼저 dry-run을
> 유지한 채 전용 zone을 확인하여 write mode와 같은 소유권 모델로 preview하세요:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --version 0.3.0 \
>   --reuse-values \
>   --set fortigate.exclusiveZoneOwnership=true \
>   --set dryRun=true
> ```
>
> 해당 계획을 검토한 다음 소유권 모델을 바꾸지 않고 쓰기를 활성화하세요:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --version 0.3.0 \
>   --reuse-values \
>   --set dryRun=false
> ```

쓰기를 켜기 전에 dry-run 상태로 업그레이드해 해당 FortiGate DNS database에 다른
컨트롤러나 운영자가 관리하는 레코드가 없는지 확인하세요. 기존 comment 기반 공유
database를 사용했다면 공유/수동 레코드를 다른 database로 옮겨야 합니다. `sources`
또는 `namespaces`로 discovery를 제한하는 경우 `cleanupPolicy=keep`을 사용하세요.
파괴적 cleanup은 완전하고 제한되지 않은 전용 database discovery에서만 허용됩니다.
제한 모드에서는 현재 레코드가 desired 상태와 정확히 일치할 때만 그대로 인정하고,
완전히 없는 이름만 생성합니다. 기존 레코드의 target, type, TTL, status 변경은
conflict로 fail-closed 처리됩니다.

차트 값은 설치 시 `values.schema.json`으로 검증되며, 모든 값의 문서(토큰
로테이션 절차, 사설 CA용 `fortigate.caBundle` 옵션, opt-in egress NetworkPolicy
포함)는 [charts/fortigate-external-dns/README.md](charts/fortigate-external-dns/README.md)에
있습니다.

공유 또는 멀티테넌트 클러스터에서는 DNS 레코드 게시를 허용할 리소스 작성자
네임스페이스만 `namespaces`로 명시하세요. 비워 두면 모든 네임스페이스를
감시하므로, Service, Ingress, Gateway, HTTPRoute 작성자가 설정된 zone에 레코드를
게시해도 되는 신뢰된 클러스터에서만 사용해야 합니다.

## 원시 매니페스트

최소 참고용 매니페스트가 `manifests/` 아래에 있습니다. 기본적으로 `default`
네임스페이스로 범위를 제한하고, 플레이스홀더 값과 Secret 참조만 사용하며, Helm
차트의 보안 기본값(비-root, 읽기 전용 루트 파일시스템, 모든 capability 드롭,
`RuntimeDefault` seccomp, 리소스 requests/limits)과 리더 선출 Lease RBAC를
그대로 반영합니다. 완전히 설정 가능한 권위 있는 산출물은 Helm 차트입니다.

원시 Deployment는 직접 단일 타깃 호환 경로입니다. 원시 매니페스트로 target
mode를 실행하려면 `manifests/crds/fortigate-external-dns.yaml`을 설치하고,
`manifests/platform-rbac.yaml`을 최소 권한으로 환경에 맞춰 적용한 뒤 Deployment에
문서화된 platform 플래그를 추가하세요. [manifests/README.md](manifests/README.md)를
참고하세요.

## 샘플

- `samples/values-existing-secret.yaml` — 미리 생성한 FortiGate API 토큰 Secret으로 설치하기 위한 Helm 값 (`helm install ... -f samples/values-existing-secret.yaml`).
- `samples/service.yaml` — 컨트롤러가 읽는 hostname/TTL 애노테이션을 보여주는 예시 `Service`.
- `samples/one-shot-plan.sh` — 현재 지원되는 canonical plan 및 exact-hash 승인 흐름.
- `samples/targets.yaml`, `samples/policy.yaml` — reference만 사용하는 활성 target-mode 타깃/정책 CR.
- `samples/ownership-adoption.yaml`, `samples/plan-approval.yaml` — 검토용 공유 adoption/approval 형태. 실제 identity 값은 컨트롤러가 생성해야 합니다.
- `samples/externalname-service.yaml`, `samples/headless-dual-stack.yaml` — IPv4/IPv6 EndpointSlice를 포함한 opt-in 소스 확장.
- `samples/monitoring-values.yaml` — metrics Service, dashboard, alert, scrape NetworkPolicy 값.
- `samples/release-verification.sh` — 게시 릴리스의 전체 증거 다운로드 및 검증.

## 릴리스 검증

모든 `v*` GitHub Release에는 패키징된 차트, 이미지/차트 SPDX 2.3 JSON SBOM,
SLSA v1 provenance bundle, 차트의 정확한 바이트에 대한 keyless Cosign bundle,
immutable 이미지 참조, 소스 커밋, `SHA256SUMS`가 포함됩니다. 이미지 서명과
이미지 attestation은 GHCR의 digest에 연결되며 mutable tag는 증거로 인정하지
않습니다. 같은 태그를 checkout한 소스에서 릴리스 자산을 내려받아 전체 검증기를
실행하세요(Cosign v3.0.6, `attestation verify`를 지원하는 `gh`, `jq` 필요):

```sh
REPOSITORY=kgskr/fortigate-externalDNS
TAG=v0.3.0
mkdir -p release-evidence
gh release download "$TAG" --repo "$REPOSITORY" --dir release-evidence
IMAGE_REF="$(cat release-evidence/IMAGE_REF)"
CHART="release-evidence/fortigate-external-dns-${TAG#v}.tgz"
scripts/verify-release-artifacts.sh \
  "$REPOSITORY" "$TAG" "$IMAGE_REF" "$CHART" release-evidence
```

검증기는 모든 checksum과 SPDX 문서를 검사한 뒤 아래와 같이 정확한 release
workflow identity 및 GitHub OIDC issuer 제약을 적용합니다. 또한 릴리스에 기록된
source tag/commit을 기준으로 SLSA provenance와 SPDX attestation을 모두 검증합니다:

```sh
IDENTITY="https://github.com/${REPOSITORY}/.github/workflows/release.yml@refs/tags/${TAG}"
ISSUER=https://token.actions.githubusercontent.com
COMMIT="$(cat release-evidence/SOURCE_COMMIT)"

cosign verify \
  --certificate-identity "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  "$IMAGE_REF"
cosign verify-blob \
  --bundle "${CHART}.sigstore.json" \
  --certificate-identity "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  "$CHART"
jq -e '.spdxVersion == "SPDX-2.3"' \
  release-evidence/image.spdx.json release-evidence/chart.spdx.json
gh attestation verify "$CHART" \
  --repo "$REPOSITORY" \
  --bundle release-evidence/chart.provenance.sigstore.json \
  --predicate-type https://slsa.dev/provenance/v1 \
  --source-ref "refs/tags/$TAG" --source-digest "$COMMIT" \
  --cert-identity "$IDENTITY" --cert-oidc-issuer "$ISSUER"
```

`make release-workflow-check release-verification-test`는 게시하지 않는 로컬 회귀
검사를 수행합니다. PR CI에 OIDC/게시 권한이 없고 수정된 바이트, 잘못된 digest,
workflow identity, issuer, repository가 모두 거부됨을 검증합니다. Fulcio 인증서
발급, transparency log 포함, GHCR attachment, GitHub Release asset 업로드는 실제
published release에서만 증명할 수 있습니다.

## 검증

```sh
make test
make static
make helm-template
make docs-samples-check
make openspec-validate
make image
make smoke
make validate
```

`make image`는 멀티스테이지 `Containerfile`로 호스트 아키텍처용 로컬 Podman 이미지를 빌드합니다(정적 바이너리는 빌더 스테이지에서 크로스컴파일됩니다). 런타임 이미지는 `gcr.io/distroless/static-debian12:nonroot` 기반으로 비-root 사용자로 실행되며 TLS 검증용 CA 인증서를 포함합니다. release 워크플로는 `v*` 태그의 GitHub Release가 published 상태가 될 때만 멀티아치 이미지(`linux/amd64`, `linux/arm64`)를 게시합니다.

`make validate`는 추가로 엄격한 baseline OpenSpec 검증, 문서 링크/명령/샘플 검증,
`make secret-scan`(추적
중인 파일에서 커밋된 API 토큰 스캔), `make secret-scan-test`(quoted key와
플레이스홀더 allowlist 회귀 테스트)를 실행합니다.

CI는 GitHub Actions로 동작합니다(`.github/workflows/` 참고): CI 워크플로가 PR과 기본 브랜치 push를 검증하고(테스트, vet, gofmt, `govulncheck`, secret scan, 스키마 검증을 포함한 Helm lint/template, 그리고 단일 아치 컨테이너 빌드 + Trivy 스캔 — 수정 가능한 HIGH/CRITICAL 발견 시 CI 실패), release 워크플로에서 재사용되어 게시를 게이트합니다. 게시는 `v*` 태그의 GitHub Release가 published 상태가 될 때만 실행되며, release 워크플로가 멀티아치 컨테이너 이미지(`linux/amd64`, `linux/arm64`)를 `ghcr.io/<owner>/fortigate-external-dns`에, Helm 차트를 GHCR OCI 아티팩트로 게시하고, 릴리스 태그를 `--version`과 `build_info` 메트릭에 스탬프합니다.

공급망 보안: Containerfile 베이스 이미지는 멀티아치 manifest-list digest로,
워크플로 액션은 커밋 SHA로 고정되며, Dependabot이 주간으로 `gomod`,
`github-actions`, `docker` 생태계를 추적해 고정값을 갱신합니다. 주간 스케줄
워크플로가 `govulncheck`를 재실행하고 마지막으로 게시된 릴리스 이미지를 Trivy로
재스캔하며, 발견 시 실행을 실패시키는 **동시에** `security-scan` 이슈를
생성/갱신하므로 릴리스 이후 CVE도 워크플로를 지켜보지 않아도 드러납니다.

## 보안 참고

- 실제 FortiGate URL, 토큰, 사설 DNS zone, 사설 IP, kubeconfig, TLS 키를 커밋하지 마세요.
- FortiGate API 자격 증명에는 Kubernetes Secret을 사용하세요.
- 먼저 `--dry-run`으로 실행하세요.
- 관리 대상 FortiGate DNS database를 이 컨트롤러 전용으로 유지하고,
  `--fortigate-exclusive-zone-ownership`을 의도적으로 확인하기 전에는 쓰기를 켜지 마세요.
- `--domain-filter`는 게시할 hostname 범위를 제한할 뿐 공유 database를 안전하게
  만들지는 않습니다.
- 공유 클러스터에서는 감시할 네임스페이스를 제한해 낮은 신뢰도의 리소스
  작성자가 FortiGate DNS 쓰기 권한을 간접적으로 얻지 않게 하세요.

## 라이선스 및 출처

이 프로젝트는 Apache License 2.0을 사용합니다. Kubernetes SIGs ExternalDNS 개념에서 영감을 받았지만, 이 저장소는 구현을 FortiGate 전용으로 유지합니다.
