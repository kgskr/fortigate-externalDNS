# FortiGate ExternalDNS

[English](README.md)

Kubernetes 리소스의 DNS 레코드를 FortiGate DNS database에 반영하는 컨트롤러입니다.
**Service, Ingress, Gateway, HTTPRoute**에서 hostname과 target을 읽고,
FortiGate API로 **A, AAAA, CNAME** 레코드를 관리합니다.

단일 FortiGate 타깃과 CRD 기반 멀티 타깃 모드를 모두 지원합니다. 공유 소유권,
정책, plan 승인 기능은 선택적으로 켤 수 있으며 플랫폼 기능은 기본 비활성화입니다.
다른 DNS 프로바이더는 지원하지 않습니다.

## 설치 전 준비

- 차트에 포함된 CRD 검증을 위한 **Kubernetes 1.31 이상**.
- FortiGate API 토큰과 **HTTPS** 주소. 사설 인증서라면 발급 CA 체인을
  `fortigate.caBundle`로 지정합니다.
- FortiGate에 미리 만든 `system dns-database`. 항목 이름과 `domain` 값이
  같아야 합니다(예: 둘 다 `example.com`). 컨트롤러는 database를 생성하지 않으며
  zone apex 레코드는 지원하지 않습니다.
- 기본 단일 타깃 모드에서는 **컨트롤러 전용 database**를 사용합니다. 기본 cleanup
  설정으로 쓰기를 켜면 수동 관리하던 A/AAAA/CNAME도 삭제될 수 있습니다.
  domain filter만으로 공유 database가 안전해지지는 않습니다.

실장비 검증 범위는 **FortiOS 7.2.11**입니다. 근거는
[검증 결과](docs/validation-results.md)에 있으며, 다른 펌웨어에서는 쓰기를 켜기 전에
dry-run으로 확인하세요.

## Helm 설치

로컬 토큰 파일로 Secret을 만듭니다:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-file=api-token=/path/to/api-token
```

실제로 사용할 전용 소유권 모델을 지정하고 dry-run으로 설치합니다:

```sh
helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.4.1 \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set fortigate.exclusiveZoneOwnership=true \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set dryRun=true
```

Secret과 Helm release는 같은 namespace에 있어야 합니다. 로컬 소스로 설치하려면
OCI 차트 주소를 `./charts/fortigate-external-dns`로 바꾸고 `--version`을 생략합니다.

## DNS 게시

LoadBalancer Service에 애노테이션을 추가하면 외부 IP나 hostname을 target으로 사용합니다:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  annotations:
    external-dns.kubernetes.io/hostname: web.example.com
    external-dns.kubernetes.io/ttl: "300"
spec:
  type: LoadBalancer
  selector:
    app: web
  ports:
    - port: 80
      targetPort: 8080
```

Ingress host와 Gateway/HTTPRoute hostname도 읽습니다. 다른 리소스 유형은
[소스 예제](samples/)를 참고하세요.

로그에서 예정된 변경을 확인합니다:

```sh
kubectl logs deployment/fortigate-external-dns
```

dry-run은 FortiGate를 변경하지 않습니다. 변경 계획과 database 전용 소유권을
확인한 뒤 쓰기를 켭니다:

```sh
helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.4.1 --reuse-values --set dryRun=false
```

`sources` 또는 `namespaces`를 제한하면 `cleanupPolicy=keep`이 필수입니다.
제한 모드에서는 새 레코드만 생성하고 기존 레코드 변경은 거부합니다. 공유 클러스터에서는
DNS 게시 권한을 줄 namespace만 감시하세요.

**업그레이드:** Helm은 CRD를 갱신하지 않습니다. 차트 버전을 바꾸기 전에 같은 태그의
CRD를 적용하세요([업그레이드 가이드](charts/fortigate-external-dns/README.md#upgrading-the-crds)).
v0.3.1 이하에서 올리면 기존 FQDN hostname과 CNAME target의 수정 계획을 dry-run으로
확인하세요. `cleanupPolicy=keep`이면 기존 행은 남으므로 직접 제거해야 합니다.

## 상세 문서

- [Helm 설정과 배포 옵션](charts/fortigate-external-dns/README.md)
- [CLI 플래그와 환경 변수](docs/configuration.ko.md)
- [멀티 타깃 전환·공유 소유권·복구·문제 해결](docs/operations.ko.md)
- [원시 매니페스트](manifests/README.md) · [일회성 plan 승인](samples/one-shot-plan.sh)
- [릴리스 검증](samples/release-verification.sh) · [개발 및 검증](docs/validation-results.md)
- [보안 제보](SECURITY.md) · [Apache 2.0 라이선스](LICENSE)
