# FortiGate ExternalDNS

📖 [English README](README.md)

FortiGate ExternalDNS는 ExternalDNS의 재조정(reconciliation) 모델에서 영감을 받은, FortiGate 전용 Kubernetes 컨트롤러입니다. 지원하는 Kubernetes 네트워킹 리소스에서 DNS 의도를 발견하고, 그 결과 DNS 레코드를 FortiGate API를 통해 FortiGate 장비에 반영합니다.

이 프로젝트는 의도적으로 FortiGate 전용입니다. Route53, Google Cloud DNS, Cloudflare, webhook 프로바이더, 서비스 메시 API, 임의의 서드파티 CRD는 지원하지 않습니다.

## 지원 소스

- Kubernetes `Service`
- Kubernetes `Ingress`
- Kubernetes SIG Gateway API `Gateway`
- Kubernetes SIG Gateway API `HTTPRoute`

Gateway API는 CRD로 설치되지만 표준 Kubernetes 네트워킹 API로 취급해 지원합니다. 그 외 CRD는 호스트네임 유사 필드를 스캔하지 않습니다.

## DNS 범위

컨트롤러는 자신이 소유한 레코드만 생성·수정·삭제합니다. 소유권은 FortiGate 레코드 메타데이터에 기록된 owner ID로 추적합니다. 클러스터마다 도메인 필터와 전용 owner ID를 사용하세요.

지원하는 레코드 타입은 타깃 값에서 유도됩니다:

- IPv4 타깃 -> `A`
- IPv6 타깃 -> `AAAA`
- DNS 이름 타깃 -> `CNAME`

### 재조정 안전성

- 플래너는 같은 zone/name/type의 비소유 레코드를 타깃이 다르더라도 충돌로
  취급합니다. 그 논리 DNS 이름에 충돌이 있는 동안 stale 소유 레코드를 부분적으로
  정리하지 않습니다.
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
`canonical-name`, `ttl`, `status`, `comment`)와 정수 레코드 키
(`q_origin_key`/`id`)는 아래 릴리스 전반에서 일관됩니다.

| FortiOS | 상태 | 비고 |
| --- | --- | --- |
| 7.0 / 7.2 / 7.4 / 7.6 | ✅ 지원 | CMDB `system/dns-database` API와 Bearer 토큰 인증이 이 릴리스 전반에서 안정적입니다. |
| 8.0 | ✅ 지원 | API 토큰은 **HTTPS 필수** — 평문 `http://`는 장비가 거부합니다. `https://` URL을 사용하세요(기본값). |
| 6.4 이하 | ⚠️ 미검증 | CMDB API와 Bearer 헤더는 6.0+부터 존재하지만, 이 릴리스들은 여기서 검증하지 않았습니다. |
| 5.6 이하 | ❌ 미지원 | 이 컨트롤러가 사용하는 Bearer 토큰 API 모델 이전 버전입니다. |

참고:

- 대상 zone은 FortiGate에 `config system dns-database` 항목으로 **미리 존재**해야 합니다(보통 primary/`master` zone). 컨트롤러는 그 zone 안에서 자신이 소유한 `dns-entry` 레코드만 관리하며, zone 자체를 생성하지 않습니다.
- FortiOS 8.0에서는 장비가 토큰 인증에 HTTPS를 강제합니다. 컨트롤러는 `https://`를 기본값으로 쓰고 `http`/`https` URL만 허용합니다. `--fortigate-insecure-skip-verify`는 인증서 검증 여부를 제어하는 별개 옵션입니다.
- 호환성은 Fortinet 공식 문서를 기준으로 검증했습니다. 특정 펌웨어에서 프로덕션 배포 전에 대상 장비를 상대로 `--dry-run --once`를 한 번 돌려보세요 — 컨트롤러가 FortiGate 응답 envelope를 검증하여 스키마/API 불일치를 안전하게 드러냅니다.

## 설정

설정은 플래그 또는 환경 변수로 제공할 수 있습니다. FortiGate 자격 증명은 Kubernetes Secret에서 가져와야 합니다.

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
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | 더 이상 대응 소스가 없는 소유 레코드의 처리 방식: `delete`(파괴적 — 레코드 삭제), `deactivate`(레코드를 비활성화하되 유지), `keep`(절대 삭제하지 않음). 초기 도입 시에는 `deactivate` 또는 `keep`을 권장합니다. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Kubernetes list 및 FortiGate 호출을 포함해 각 재조정 루프에 시간 상한을 둡니다. |
| `--leader-election` | `LEADER_ELECTION` | `true` | 다중 레플리카 배포를 위한 Lease 기반 단일 쓰기 가드. `--once`에서는 무시됩니다. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease 이름. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | 파드 네임스페이스 | Lease가 위치할 네임스페이스. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | `/healthz`, `/readyz`, `/metrics`의 바인드 주소. 비우면 서버 비활성화. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (없음) | 부모 Gateway 주소 해석에만 참조하는 추가 네임스페이스. 조회 범위 전용이며 소유권/정리(cleanup) 범위를 넓히지 않습니다. 네임스페이스 한정 설치 시 Helm 차트가 이 네임스페이스마다 읽기 전용 `gateways` Role을 자동 생성합니다. |

메트릭은 `fortigate_external_dns_` 접두사로 Prometheus 텍스트 형식으로 노출됩니다
(재조정 카운터, 재조정 소요 시간 히스토그램, type/result 라벨이 붙은 작업 카운터 —
`planned`, `applied`, `failed`, `skipped`, `conflict` — 마지막 성공 재조정
타임스탬프). 토큰이나 레코드 페이로드는 노출하지 않습니다.

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

먼저 Secret을 만듭니다:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token='<fortigate-api-token>'
```

차트로 설치합니다:

```sh
helm install fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com
```

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

## 샘플

- `samples/values-existing-secret.yaml` — 미리 생성한 FortiGate API 토큰 Secret으로 설치하기 위한 Helm 값 (`helm install ... -f samples/values-existing-secret.yaml`).
- `samples/service.yaml` — 컨트롤러가 읽는 hostname/TTL 애노테이션을 보여주는 예시 `Service`.

## 검증

```sh
make test
make static
make helm-template
make image
make smoke
make validate
```

`make image`는 멀티스테이지 `Containerfile`로 호스트 아키텍처용 로컬 Podman 이미지를 빌드합니다(정적 바이너리는 빌더 스테이지에서 크로스컴파일됩니다). 런타임 이미지는 `gcr.io/distroless/static-debian12:nonroot` 기반으로 비-root 사용자로 실행되며 TLS 검증용 CA 인증서를 포함합니다. release 워크플로는 `v*` 태그의 GitHub Release가 published 상태가 될 때만 멀티아치 이미지(`linux/amd64`, `linux/arm64`)를 게시합니다.

`make validate`는 추가로 `make secret-scan`(추적 중인 파일에서 커밋된 API 토큰을
스캔)과 `make secret-scan-test`(플레이스홀더 allowlist 회귀 테스트)를 실행합니다.

CI는 GitHub Actions로 동작합니다(`.github/workflows/` 참고): CI 워크플로가 PR과 기본 브랜치 push를 검증하고(테스트, vet, gofmt, secret scan, Helm lint/template), release 워크플로에서 재사용되어 게시를 게이트합니다. 게시는 `v*` 태그의 GitHub Release가 published 상태가 될 때만 실행되며, release 워크플로가 멀티아치 컨테이너 이미지(`linux/amd64`, `linux/arm64`)를 `ghcr.io/<owner>/fortigate-external-dns`에, Helm 차트를 GHCR OCI 아티팩트로 게시합니다.

## 보안 참고

- 실제 FortiGate URL, 토큰, 사설 DNS zone, 사설 IP, kubeconfig, TLS 키를 커밋하지 마세요.
- FortiGate API 자격 증명에는 Kubernetes Secret을 사용하세요.
- 먼저 `--dry-run`으로 실행하세요.
- 관련 없는 레코드를 건드리지 않도록 `--domain-filter`와 `--owner-id`를 사용하세요.
- 공유 클러스터에서는 감시할 네임스페이스를 제한해 낮은 신뢰도의 리소스
  작성자가 FortiGate DNS 쓰기 권한을 간접적으로 얻지 않게 하세요.

## 라이선스 및 출처

이 프로젝트는 Apache License 2.0을 사용합니다. Kubernetes SIGs ExternalDNS 개념에서 영감을 받았지만, 이 저장소는 구현을 FortiGate 전용으로 유지합니다.
