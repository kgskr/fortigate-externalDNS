## Context

The controller now has the basic FortiGate-only ExternalDNS shape, but the review correctly identified that DNS mutation safety matters more than code tidiness. The most important risks are in identity, ordering, partial failure, configuration parsing, and runtime coordination. A bad reconcile loop can leave duplicate FortiGate records, block later operations forever, or perform real writes when an operator intended dry-run mode.

The repository must remain public-GitHub ready, but GitHub Actions workflows must not be added in this change. Validation should remain local and documented through commands, tests, scripts, and OpenSpec tasks.

## Goals / Non-Goals

**Goals:**

- Make DNS diff/apply behavior safe when targets change, operations partially fail, or FortiGate returns structured errors inside successful HTTP responses.
- Make runtime behavior observable and Kubernetes-friendly through single-writer coordination, health/readiness endpoints, metrics, and timeout boundaries.
- Make source publishing behavior explicit for Service and Gateway API resources, especially unsupported service types and shared infrastructure Gateway namespaces.
- Keep Helm, raw manifests, README, and validation docs consistent without enabling GitHub workflow execution.
- Expand test coverage around the reviewed failure modes.

**Non-Goals:**

- Do not add any non-FortiGate DNS provider.
- Do not add service mesh, arbitrary CRD, or generic hostname scanning support.
- Do not add GitHub Actions workflow files.
- Do not silently publish ClusterIP, headless, or NodePort Service records without an explicit policy and tests.

## Decisions

### Decision: Treat DNS record identity and FortiGate entry identity separately

The planner should distinguish logical DNS intent from FortiGate entry identity. FortiGate entries may still be one target per entry, but an IP replacement for the same hostname and type must not be treated as an unordered create+delete pair that can leave duplicates.

Alternatives considered:

- Remove target from `Endpoint.Key()`. This breaks multi-A/AAAA records because target-per-entry endpoints overwrite each other.
- Keep target in the key and rely on create+delete ordering. This preserves multi-target records but is unsafe when delete fails after create succeeds.

Chosen approach: introduce replacement-aware planning. For same zone/name/type and same owner/source scope, detect target changes as replacements. Prefer FortiGate PUT in place when a reliable provider ID exists for a 1:1 replacement; fall back to delete-before-create only when no provider ID is available or the change is not a clean 1:1 replacement. Replacement operations must be ordered and tested separately from ordinary creates and stale deletes.

### Decision: Apply operations fail-soft and report aggregate results

FortiGate apply should continue independent operations after an individual record fails, then return an aggregate error using `errors.Join` or equivalent. Logs should include attempted, succeeded, failed, skipped, and conflict counts.

Alternatives considered:

- Stop on first error. This keeps the implementation simple but lets one bad record permanently block unrelated records.
- Ignore per-record errors. This hides operational failures.

Chosen approach: continue safe independent operations, aggregate errors, and keep enough counters for logs and metrics.

### Decision: Malformed environment variables are configuration errors

If a non-empty environment variable is malformed, startup must fail. This is especially important for `DRY_RUN`, where `ture` must not fall back to `false`.

Alternatives considered:

- Keep current default fallback behavior. This is convenient but unsafe.
- Validate only dangerous variables. This leaves inconsistent behavior and hidden misconfiguration.

Chosen approach: all typed env parsing should fail on malformed non-empty values.

### Decision: Add leader election rather than only documenting replicaCount=1

The chart default can remain one replica, but the controller should protect itself when multiple replicas exist. Use Kubernetes Lease-based leader election through client-go so only the leader performs reconciliation. Health endpoints can remain live for all pods; readiness should indicate whether the pod is able to serve its configured role.

Alternatives considered:

- Reject `replicaCount > 1` only in Helm. This does not protect raw manifests or manual scaling.
- Rely on documentation. This is insufficient for a DNS-mutating controller.

Chosen approach: add optional leader election enabled by default in Kubernetes deployments, with flags and Helm values to disable only for local testing. `--once` runs bypass leader election entirely.

### Decision: Separate Gateway target lookup scope from cleanup ownership scope

Namespace filters define which source resources may publish DNS and which owned records may be cleaned up. Gateway target lookup may need to read shared infrastructure namespaces so an HTTPRoute in an app namespace can resolve a parent Gateway in an infra namespace. Add explicit Gateway target namespace configuration instead of conflating target lookup with ownership cleanup.

Alternatives considered:

- Require all Gateway parents to be in source namespaces. This blocks a common Gateway API topology.
- Read all namespaces unconditionally. This increases RBAC and surprises namespace-scoped installs.

Chosen approach: add `gateway-target-namespace` configuration. If omitted, use source namespaces. Operators can list shared infra namespaces explicitly.

### Decision: Service publishing must be explicit

The current Service source only emits LoadBalancer status and ExternalIPs. That is safe but silent for common Service types. Keep conservative defaults, but log/report unsupported types and add an explicit service publish policy before publishing ClusterIP, headless, or NodePort targets.

Alternatives considered:

- Publish ClusterIP/NodePort by default. This can create DNS records that are not meaningful outside the cluster.
- Keep silent ignore. This makes debugging difficult.

Chosen approach: keep publishing limited to LoadBalancer status addresses and ExternalIPs in this change. Unsupported Service types (ClusterIP, headless, NodePort) that carry a hostname annotation emit a warning naming the type and publish no record. No new publish modes are added here.

### Decision: Ship restricted-PSS container and pod hardening

Helm chart and raw manifests should satisfy the Kubernetes restricted Pod Security Standard and document the security posture, since this is a network-mutating controller. Add `seccompProfile: { type: RuntimeDefault }` (the field currently missing for restricted PSS), and additionally apply `readOnlyRootFilesystem: true` and default resource requests/limits as recommended hardening. Pin the controller image by digest, or document an explicit tag-pinning policy. Raw manifests either match these defaults or are clearly marked as minimal examples.

Alternatives considered:

- Leave hardening to the operator. This is unsafe-by-default for a controller with write access to a firewall.
- Pin only by tag. Tags are mutable; prefer digest pinning or a documented policy.

Chosen approach: bake restricted-PSS defaults plus readOnlyRootFilesystem and resource requests/limits into the chart, mark raw manifests as minimal examples where they diverge, and document image pinning.

## Risks / Trade-offs

- Replacement-aware planning can become too complex -> Keep replacement operations explicit in the plan model and cover create/update/delete/replacement ordering in unit tests.
- Leader election adds dependency and RBAC complexity -> Scope RBAC to `coordination.k8s.io/leases` and make local non-Kubernetes execution still possible.
- Gateway target namespaces can over-grant RBAC -> Require explicit Helm values and document that target namespaces are read-only lookup scope, not cleanup scope.
- Metrics can expose too much detail -> Publish counts and durations, not FortiGate tokens or sensitive record payloads.
- A metrics library adds the first non-Kubernetes dependency -> Use a minimal Prometheus client or a hand-rolled text-exposition handler; do not pull in broad frameworks.
- FortiGate envelope format may vary by FortiOS version -> Implement tolerant parsing for known `status`, `http_status`, `error`, and `message` fields and add fixtures for accepted variants.

## Migration Plan

1. Add tests that reproduce the reviewed safety failures before changing behavior.
2. Update DNS endpoint identity and planner operations to support safe replacements.
3. Update FortiGate apply to aggregate errors and validate response envelopes.
4. Update config parsing to fail on malformed typed environment values.
5. Add reconcile timeout, context-aware retry, health/readiness/metrics, and leader election.
6. Add Gateway target namespace configuration and Service unsupported-type reporting.
7. Align Helm values/templates, raw manifests, README, validation docs, and tests.
8. Run local validation and subagent review before committing.

Rollback is a normal Git revert of this hardening change. The change should reduce write risk; it should not require data migration in FortiGate.

## Resolved Questions

- **Replacement strategy:** Prefer FortiGate PUT in place when a reliable provider ID exists for a 1:1 replacement of the same zone/name/type (one owned current target -> one desired target). This avoids both duplicate entries and any window where the record is absent. Fall back to delete-before-create only when no provider ID is available or the change is not a clean 1:1 replacement (for example a partial change within a multi-target set). Multi-target sets that are not clean replacements are handled as independent create and delete operations.
- **Stable metric surface:** All metrics use the `fortigate_external_dns_` prefix. The documented stable set is: reconcile total and error counters, a reconcile duration histogram, applied-operation counters labeled by operation type and result, and a last-successful-reconcile timestamp gauge. Names outside this documented set may change without notice.
- **Leader election:** Default-on for long-running in-cluster mode, disableable via flag and Helm value for local testing. `--once` runs bypass leader election entirely, since a one-shot run has no leader to elect. Health stays live on all pods; only the leader applies DNS changes.
- **Service publish modes:** This change adds no new publishing modes. Only LoadBalancer status addresses and ExternalIPs are published. ClusterIP, headless, and NodePort Services that carry a hostname annotation emit a warning event/log naming the unsupported type and publish no record.
