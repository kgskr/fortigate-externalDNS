## Context

The controller currently performs a full Kubernetes discovery and FortiGate list on a fixed interval, builds an in-memory plan, logs dry-run operations, and mutates one configured VDOM/zone. Write mode requires exclusive ownership because FortiOS exposes no documented per-record metadata field. Metrics and Kubernetes events expose loop-level outcomes, but there is no durable plan, ownership registry, target status, policy object, or artifact attestation.

The expansion must preserve the current fail-closed behavior: incomplete source or provider state cannot authorize cleanup; CNAME and address conflicts remain errors; failed prerequisite mutations block dependent cleanup; secrets never enter plans, status, logs, or metrics; and the existing single-target installation remains usable without CRDs beyond the already optional Gateway API.

## Goals / Non-Goals

**Goals:**

- Make every planned and applied change deterministic, reviewable, attributable, and auditable.
- Permit shared-zone coexistence through durable Kubernetes ownership claims using documented FortiGate fields only.
- Enforce publication policy before desired endpoints reach the planner.
- Reconcile promptly from Kubernetes changes while retaining full-audit cleanup safety.
- Isolate multiple FortiGate targets and expose actionable status for each target and source.
- Add explicitly supported ExternalName and headless Service publication modes.
- Publish cryptographically verifiable images and Helm artifacts with SBOM and provenance.

**Non-Goals:**

- Supporting DNS providers other than FortiGate.
- Treating FortiGate as a transactional datastore or promising atomic updates across targets.
- Publishing NodePort Services, wildcard hostnames, arbitrary CRDs, or service-mesh resources.
- Storing API tokens, CA private material, or Secret values in CRDs, plans, logs, metrics, or audit history.
- Replacing Kubernetes RBAC, admission control, or external policy engines.
- Building a web UI.

## Decisions

### Deliver one compatibility-preserving platform layer

The implementation is one OpenSpec change because its safety contracts cross ownership, planning, scheduling, policy, status, packaging, and release verification. Work is phased and independently testable, but the new shared-zone write mode is not considered complete until the registry, plan, policy, status, RBAC, migration, and failure-path tests are integrated.

The existing flags synthesize a `default` target internally. Installing target CRDs does not switch modes automatically. CRD-managed multi-target mode is enabled explicitly and is mutually exclusive with direct FortiGate credential flags.

Alternative considered: separate unrelated changes. Rejected for the shared-zone path because partially shipping the registry without plan/status/policy and packaging gates would create an unsafe intermediate operating model.

### Canonical plan documents are the mutation contract

Add a versioned `plan.Document` containing target identity, provider snapshot revision, discovery generation, ownership-registry resource versions, policy generation, sorted operations, prerequisite edges, and safety decisions. Canonical JSON excludes timestamps, outcomes, secrets, and unstable map ordering. The plan ID is SHA-256 over the canonical bytes.

Every apply consumes a document and rejects a changed hash, expired provider snapshot, changed ownership claim, changed policy generation, or target mismatch. Approval gating is disabled by default. When enabled, long-running mode persists a `FortiGateDNSChangePlan` in the controller namespace and requires an exact approval-hash annotation before apply. `--once` supports writing canonical JSON to a file and applying only an explicitly supplied hash. Audit status records per-operation outcomes and terminal phase with bounded retention.

Alternative considered: hash the current log strings. Rejected because strings are not a stable API and omit prerequisite and snapshot state.

### Shared-zone ownership uses namespaced claims with two-phase confirmation

`FortiGateDNSRecordOwnership` is stored in the controller namespace and named from a hash of target plus normalized logical record key. Its spec includes target reference, provider ID when known, normalized record fingerprint, source object UID(s), controller ID, and adoption intent. Status records phase (`Reserved`, `Confirmed`, `Orphaned`, `Conflict`), observed provider revision, and last confirmation time.

Create reserves a claim before the provider request and confirms it after the created row is observed. Update, replace, delete, and deactivate require a confirmed claim whose provider ID/fingerprint/resourceVersion matches the plan. Lost responses converge by relisting and confirming an exact reserved fingerprint. Adoption is opt-in and succeeds only for an exact live fingerprint and an unclaimed logical record. Claim updates use Kubernetes resourceVersion conflicts as the coordination boundary. Orphan claims never authorize cleanup until re-confirmed.

Exclusive-zone mode remains available and does not require claims. Shared-zone mode never infers ownership from hostname, target, an undocumented FortiOS field, or another controller's claim.

Alternative considered: a single ConfigMap registry. Rejected because of size limits, coarse conflicts, and a single corruption domain.

### Policies are evaluated as a restrictive intersection

`FortiGateDNSPolicy` is namespaced and selects source kinds and optional labels. Matching policies contribute allowed hostname suffixes, TTL bounds, target CIDRs/hostname suffixes, record quotas, and opt-in requirements. Deny wins; numeric bounds use the strictest intersection; an empty intersection rejects publication. Global flags remain an outer restriction and cannot be widened by policy. With no policy objects and policy enforcement disabled, current behavior is unchanged.

Policy rejection produces a Kubernetes event, status reason, and bounded metric label; it does not mark discovery incomplete and therefore cannot preserve a record that policy intentionally removed. Policy configuration/read failures do mark the policy source incomplete and suppress cleanup.

Alternative considered: first-match policy. Rejected because ordering would make independent administrators able to accidentally widen access.

### Events enqueue target audits; cleanup remains full-audit-only

Shared informers watch enabled source resources, EndpointSlices, targets, policies, ownership claims, change plans, and referenced Secret metadata. Handlers map events to affected target keys and add them to a rate-limited workqueue. Duplicate keys coalesce. A target worker performs a full cached Kubernetes discovery and FortiGate snapshot; this keeps the existing planner contract and makes event-driven latency an optimization rather than a new partial-cleanup algorithm.

Periodic resync enqueues every target and is the fallback for missed events and external FortiGate drift. Cleanup is allowed only when the triggering run completes all configured source lists, policy lists, ownership reads, and a stable provider snapshot. Queue retries use capped exponential backoff with jitter; success forgets the key. Shutdown drains no new mutations after leadership is lost.

Alternative considered: per-object incremental provider mutation. Rejected because deletion, policy changes, overlapping sources, and shared ownership require a complete desired set to prove stale records.

### Multi-target mode is CRD-driven and isolated

`FortiGateDNSTarget` is namespaced in the controller namespace. It references an API-token Secret key and optional CA ConfigMap/Secret key, and declares URL, VDOM, zone, ownership mode, source/namespace/domain selectors, cleanup policy, reconcile settings, and plan approval mode. Secret values are resolved only in memory. Each target has a separately constructed client, queue key, plan, ownership namespace, status, metrics labels, retry state, and circuit breaker.

Write-enabled targets with overlapping domain suffixes are rejected unless both are `keep` and explicitly marked read/create-only. One target failure does not block another target. The initial implementation uses one process-wide leader lease so only one replica mutates any target; per-target leases are deferred until target sharding is designed.

Alternative considered: only one deployment per target. Retained as a supported simple topology, but insufficient for fleets with many VDOM/zone pairs and repeated controller overhead.

### Status is CRD-backed and metric labels stay bounded

`FortiGateDNSStatus` is one namespaced object per target. Status contains conditions for Ready, DiscoveryComplete, ProviderReachable, OwnershipHealthy, PolicyAccepted, PlanApproved, and DriftFree; observed generations/revisions; desired/current/conflict counts; last plan hash; last audit/apply timestamps; and a bounded ring of summary outcomes. It never contains targets from Secret data, HTTP bodies, tokens, or full record dumps.

Prometheus adds target-name labels only after validating target names as bounded Kubernetes object names. Source and reason labels use fixed enumerations. Optional chart assets include a ServiceMonitor-compatible Service, PrometheusRule examples, and a Grafana dashboard ConfigMap without requiring those CRDs.

Alternative considered: Events and logs only. Rejected because they are transient and do not provide a queryable current condition.

### Source expansion is explicit and EndpointSlice-based

ExternalName Services publish the annotated hostname as a CNAME to `spec.externalName` only when the new mode is enabled and the target is a valid non-IP hostname. Headless Services require an explicit annotation or policy grant and obtain ready addresses from EndpointSlices selected by the standard service-name label. `publishNotReadyAddresses` permits endpoints whose readiness is false; otherwise only ready or readiness-unknown serving endpoints are used. Hostname targets continue to win over IP targets, and existing CNAME/address conflict checks remain authoritative.

NodePort, ClusterIP, wildcard, SRV, and arbitrary CRD publication stay unsupported.

Alternative considered: legacy Endpoints. Rejected because EndpointSlice is the scalable Kubernetes API and carries address family/readiness information.

### Release verification uses keyless signing and immutable evidence

Release jobs generate an SPDX JSON SBOM for the built image and packaged chart, attach both to the GitHub release, generate SLSA-compatible provenance using GitHub OIDC, and sign image digests and chart archives with keyless Cosign. Verification commands and identity/issuer constraints are documented and exercised in CI without publishing from pull requests.

Alternative considered: repository-held signing keys. Rejected because keyless OIDC avoids long-lived signing secrets and provides workflow identity evidence.

## Risks / Trade-offs

- **[CRD loss could remove ownership evidence]** → Claims use finalizers, backup documentation, orphan-safe behavior, and never cause provider deletion solely because a claim disappeared.
- **[Approval plans become stale]** → Hash includes provider revision and resource generations; stale plans are rejected and regenerated.
- **[Informer event storms overload FortiGate]** → Per-target coalescing, minimum debounce, rate limiting, and one in-flight reconcile per target.
- **[Policy changes intentionally remove many records]** → Existing empty-desired and cleanup-count guards still apply, and approval mode can be required per target.
- **[Multi-target credentials increase blast radius]** → Secret references are per target, RBAC is namespace-scoped, clients and failures are isolated, and no secret material enters status.
- **[CRD version evolution becomes a compatibility obligation]** → Start at `v1alpha1`, structural schemas, conversion-free single served/storage version, and document upgrade guarantees before v1beta1.
- **[Headless endpoints churn rapidly]** → Workqueue coalescing and deterministic target sorting prevent unstable plans.
- **[Keyless verification depends on GitHub/OIDC availability]** → Publishing fails closed; ordinary local builds and tests remain independent.
- **[Change breadth increases integration risk]** → Implement by task phase, require unit tests per package, run race tests and rendered-artifact checks after every integration boundary, and do not enable shared/multi-target defaults until the complete gate passes.

## Migration Plan

1. Ship canonical plan/audit primitives, status metrics, CRDs, and RBAC while all new modes remain disabled.
2. Install CRDs and enable status-only observation for the synthesized default target; verify no secret or cardinality regressions.
3. Enable event-driven enqueueing while retaining the existing periodic interval as full resync.
4. For a shared zone, start dry-run, create exact-match adoption candidates, review plan hashes, approve adoption, and wait for all claims to become Confirmed before enabling mutation.
5. Enable policies namespace by namespace, beginning with report-only diagnostics and then enforcement.
6. Migrate additional single-target releases into `FortiGateDNSTarget` objects one at a time; reject overlapping write scopes before disabling the old deployment.
7. Enable ExternalName/headless publication explicitly and review the first plan before writes.
8. Publish signed artifacts only after verification jobs pass; keep prior signed digests available for rollback.

Rollback disables approval/shared/multi-target/source-expansion modes and returns to the legacy synthesized target. Confirmed ownership CRDs are retained and ignored rather than deleted. A rollback MUST use dry-run until the operator verifies that the old exclusive-zone assumptions are valid; the old controller MUST NOT clean a shared zone.

## Open Questions

None blocking. Per-target leader leases, target sharding across replicas, SRV records, wildcard records, and a validating admission webhook are deferred to later changes.
