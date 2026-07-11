# Platform requirement evidence

Generated from the implemented OpenSpec change and reviewed on 2026-07-11. Every scenario is tied to an automated repository test/check or, only where GitHub OIDC is inherently required, an explicit external release gate.

| Capability | Requirement | Scenario | Evidence |
|---|---|---|---|
| `controller-operability` | Event-driven target workqueue | Source object changes | `internal/controller/platform_runtime_test.go; internal/workqueue/*_test.go` |
| `controller-operability` | Event-driven target workqueue | Status-only update is irrelevant | `internal/controller/platform_runtime_test.go; internal/workqueue/*_test.go` |
| `controller-operability` | Periodic full audit remains authoritative | External provider drift occurs | `internal/controller/platform_runtime_test.go; internal/workqueue/*_test.go` |
| `controller-operability` | Bounded retry and debounce | Event storm coalesces | `internal/controller/platform_runtime_test.go; internal/workqueue/*_test.go` |
| `controller-operability` | Leadership loss stops mutation | Leadership is lost during apply | `internal/controller/platform_runtime_test.go; internal/workqueue/*_test.go` |
| `deployment-artifact-consistency` | CRDs and least-privilege RBAC are packaged | Default Helm render includes CRDs safely | `scripts/helm-template-check.sh; scripts/platform-artifact-check.rb; charts/fortigate-external-dns` |
| `deployment-artifact-consistency` | CRDs and least-privilege RBAC are packaged | RBAC creation is disabled | `scripts/helm-template-check.sh; scripts/platform-artifact-check.rb; charts/fortigate-external-dns` |
| `deployment-artifact-consistency` | Compatibility-safe Helm values and schema | Existing values render unchanged | `scripts/helm-template-check.sh; scripts/platform-artifact-check.rb; charts/fortigate-external-dns` |
| `deployment-artifact-consistency` | Raw manifests remain a supported legacy path | Raw manifest validation | `scripts/helm-template-check.sh; scripts/platform-artifact-check.rb; charts/fortigate-external-dns` |
| `deployment-artifact-consistency` | Generated and documented artifacts stay synchronized | CRD or values drift occurs | `scripts/helm-template-check.sh; scripts/platform-artifact-check.rb; charts/fortigate-external-dns` |
| `dns-policy-governance` | Namespace policy restricts publication | Multiple policies intersect | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `dns-policy-governance` | Namespace policy restricts publication | Empty intersection denies publication | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `dns-policy-governance` | Deny and invalid policy fail closed | Policy API becomes unavailable | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `dns-policy-governance` | Explicit publication opt-in | Required opt-in is absent | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `dns-policy-governance` | Per-namespace and per-target quotas | Quota is exceeded | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `dns-policy-governance` | Compatibility when governance is disabled | Upgrade without policy objects | `internal/policy/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `multi-target-management` | Declarative FortiGate targets | Target Secret is resolved | `internal/target/*_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `multi-target-management` | Declarative FortiGate targets | Secret reference is missing | `internal/target/*_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `multi-target-management` | Target failure isolation | Concurrent healthy and failing targets | `internal/target/*_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `multi-target-management` | Overlapping write scopes are rejected | Parent and child suffix overlap | `internal/target/*_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `multi-target-management` | Legacy single-target compatibility | Legacy deployment starts after upgrade | `internal/target/*_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `reconciliation-data-safety` | Destructive operations require a fresh complete audit | Informer cache is not synchronized | `internal/controller/runner_test.go; internal/controller/platform_runtime_test.go; internal/ownership/*_test.go` |
| `reconciliation-data-safety` | Destructive operations require a fresh complete audit | Non-destructive event run lacks cleanup evidence | `internal/controller/runner_test.go; internal/controller/platform_runtime_test.go; internal/ownership/*_test.go` |
| `reconciliation-data-safety` | Apply revalidates plan identity and ownership | Ownership changes mid-apply | `internal/controller/runner_test.go; internal/controller/platform_runtime_test.go; internal/ownership/*_test.go` |
| `reconciliation-data-safety` | Existing cleanup guards remain cumulative | Approved plan exceeds cleanup cap | `internal/controller/runner_test.go; internal/controller/platform_runtime_test.go; internal/ownership/*_test.go` |
| `reconciliation-data-safety` | Cross-target operations are never atomic dependencies | One target apply fails | `internal/controller/runner_test.go; internal/controller/platform_runtime_test.go; internal/ownership/*_test.go` |
| `reconciliation-status` | Per-target current status | Target becomes healthy | `internal/status/writer_test.go; internal/metrics/platform_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `reconciliation-status` | Per-target current status | One target fails independently | `internal/status/writer_test.go; internal/metrics/platform_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `reconciliation-status` | Status and history are bounded and sanitized | Provider returns a sensitive error body | `internal/status/writer_test.go; internal/metrics/platform_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `reconciliation-status` | Expanded bounded metrics | Metrics cardinality remains bounded | `internal/status/writer_test.go; internal/metrics/platform_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `reconciliation-status` | Optional operator assets | Monitoring assets are disabled | `internal/status/writer_test.go; internal/metrics/platform_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Confirmed claim authorizes existing-record mutation | Matching confirmed claim permits mutation | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Confirmed claim authorizes existing-record mutation | Missing claim blocks mutation | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Two-phase create ownership | Lost create response converges safely | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Explicit exact-match adoption | Exact unclaimed record is adopted | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Explicit exact-match adoption | Changed adoption candidate is refused | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Claim conflicts and orphaning fail closed | Ownership object is deleted unexpectedly | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `shared-zone-ownership` | Exclusive mode remains compatible | Existing installation upgrades unchanged | `internal/ownership/*_test.go; cmd/fortigate-external-dns/shared_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `source-publishing-scope` | Opt-in ExternalName publication | Valid ExternalName is published | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | Opt-in ExternalName publication | IP or malformed ExternalName is rejected | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | Opt-in headless EndpointSlice publication | Ready dual-stack endpoints are published | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | Opt-in headless EndpointSlice publication | Unready endpoint is excluded by default | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | Opt-in headless EndpointSlice publication | Publish-not-ready is intentional | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | EndpointSlice discovery participates in cleanup safety | EndpointSlice API list fails | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `source-publishing-scope` | Unsupported source modes remain explicit | NodePort Service requests publication | `internal/source/service_expansion_test.go; internal/source/kubernetes_test.go` |
| `structured-plan-audit` | Canonical reconciliation plan | Equivalent inputs produce the same plan | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `structured-plan-audit` | Canonical reconciliation plan | Sensitive values are absent | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `structured-plan-audit` | Optional exact-hash approval | Matching approval permits apply | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `structured-plan-audit` | Optional exact-hash approval | Missing or different approval blocks apply | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `structured-plan-audit` | Stale plans fail closed | Provider changes after approval | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `structured-plan-audit` | Durable bounded audit outcome | Partial independent progress is recorded | `internal/plan/*_test.go; internal/controller/runner_test.go; cmd/fortigate-external-dns/platform_integration_test.go` |
| `supply-chain-security` | Released artifacts are digest signed | Release signing succeeds | `scripts/release-workflow-check_test.sh; scripts/verify-release-artifacts_test.sh; external release.published OIDC gate` |
| `supply-chain-security` | SPDX SBOMs accompany releases | SBOM generation fails | `scripts/release-workflow-check_test.sh; scripts/verify-release-artifacts_test.sh; external release.published OIDC gate` |
| `supply-chain-security` | Provenance is verifiable | Artifact does not match provenance | `scripts/release-workflow-check_test.sh; scripts/verify-release-artifacts_test.sh; external release.published OIDC gate` |
| `supply-chain-security` | Pull requests do not publish trust evidence | Untrusted pull request runs | `scripts/release-workflow-check_test.sh; scripts/verify-release-artifacts_test.sh; external release.published OIDC gate` |

## Cross-cutting audit

- Compatibility defaults: config, target, Helm default-render, and legacy integration tests.
- Fail-closed cleanup: source incompleteness, policy API failure, cache sync, stable snapshot, empty desired, cleanup cap, and claim precondition tests.
- Secret exclusion: config redaction, target credential, plan serialization, status sanitization, metric cardinality, sample, and secret-scan tests.
- Target isolation: runtime manager, routing, platform integration, retry, rotation, and race tests.
- Shared ownership: two-phase create, exact adoption, mutation revalidation, lost-response, dry-run, and conflict-race tests.
- Release trust: pinned workflow, OIDC permissions, digest-only signing, SBOM/provenance presence, and positive/negative verification scripts; actual OIDC issuance remains a release-published gate.
