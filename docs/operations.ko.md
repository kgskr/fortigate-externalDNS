# 운영 가이드

[README](../README.ko.md) · [Helm 설정](../charts/fortigate-external-dns/README.md)

## 안전 불변조건

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
  허용한 비파괴 모드가 아닌 한 잘못된 설정입니다. 잘못되었거나 겹치는 타깃은 제외되어
  status에 보고되고, 정상 타깃은 계속 재조정됩니다.
- 검증에 실패한 `FortiGateDNSPolicy`는 해당 namespace의 게시를 거부하고, 고쳐질
  때까지 모든 cleanup을 중단합니다. 다른 namespace는 계속 게시됩니다.

## 클러스터 레코드 해체(decommissioning)

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

## 마이그레이션 및 복구

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

[samples](../samples/)의 adoption/approval CR은 검토용 형태일 뿐입니다. 실제 fingerprint,
revision, canonical document와 hash는 컨트롤러가 생성해야 합니다.

### Legacy에서 멀티 타깃으로

기존 Deployment마다 dry-run `FortiGateDNSTarget` 하나를 만들고 Secret/CA key
reference만 사용합니다. 기존 source, namespace, domain, VDOM, zone, cleanup,
controller identity 경계를 보존합니다. 쓰기 DNS 범위가 겹치지 않는지 검증하세요.
dry-run 타깃은 writer가 아니지만, 의도적인 비파괴 overlap은 양쪽 모두
`cleanupPolicy=keep`과 `allowNonDestructiveOverlap=true`가 필요합니다. 타깃을
독립적으로 검토하고 하나씩 활성화해 한 타깃의 인증/TLS/API/정책 실패가 다른
타깃의 변경 권한으로 이어지지 않게 합니다.

Target mode를 켜거나 업그레이드하기 전에 API 토큰 Secret 관리자가
`fortigate-external-dns.kgskr.io/token-target`(`namespace/name`),
`token-url`(정확한 `spec.url`), `token-key`(참조한 키)를 annotation으로
추가해야 합니다. `spec.caRef`가 있으면 소문자 `kind/name/key` 형식의
`token-ca-ref`도 추가합니다(예: `configmap/edge-ca/ca.crt`). 누락되거나
일치하지 않으면 해당 타깃은 `token-binding-mismatch`로 시작하지 않습니다.
Target mode에서는 TLS 검증이 필요하며 사설 인증서에는 CA reference를
사용합니다.

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
