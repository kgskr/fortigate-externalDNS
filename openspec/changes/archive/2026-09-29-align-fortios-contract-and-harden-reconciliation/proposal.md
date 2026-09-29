## Why

The first live run against a FortiGate (FortiOS v7.2.11) showed that the provider contract was written against assumed documentation. FortiOS serves `dns-entry` hostnames and undotted `canonical-name` values relative to the zone, so every record the controller wrote was published as `name.zone.zone`. List responses carry no `limit_reached` field, so every reconcile failed before planning. A follow-up review also found paths that delete records the controller cannot recreate (NS/MX rows, stale HTTPRoute status, invalid policies), approval loops, wedged ownership claims, liveness restarts in target mode, crash loops from one bad target, and chart configurations that run several writers against one exclusive database.

## What Changes

- Encode names the way FortiOS serves them: zone-relative hostnames, absolute dotted CNAME targets, and rejection of apex, out-of-zone, and unsupported-type writes.
- Rebuild list pagination on the observed `size`/per-page `matched_count` semantics, validate provider IDs and URL paths, bound response bodies, and pace retries with jittered backoff and `Retry-After`.
- Never adopt non-A/AAAA/CNAME rows, treat multiple CNAME targets as a conflict, and suppress cleanup when HTTPRoute status is stale or a policy is invalid.
- Skip plans and approval for cycles with no actionable operation, stale earlier approvals on such cycles, and record terminal plan phases after a reconcile timeout.
- Intersect HTTPRoute hostnames with attached listeners, pair each name with its own parents' addresses, validate hostnames (IDNA, DNS-1123, apex), accept duration TTLs, and reject unmatchable domain filters.
- Isolate invalid or overlapping targets, trim Secret tokens, separate per-request and reconcile timeouts, keep target-mode liveness and metrics truthful, release the leader Lease only after work drains, and route client-go logs through the structured logger.
- Release deleted shared-mode claims, recover orphaned and interrupted claims safely, and derive status conditions from the actual audit.
- Harden the chart (writer guards, Job for one-shot, trimmed RBAC, egress lists and selectors, ServiceMonitor and aggregated alerts, PDB and scheduling knobs, CRD validation and upgrade docs) and CI (staticcheck, toolchain pin, tool versions, release version guard, grouped Kubernetes updates, security policy).

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `reconciliation-data-safety`: FortiOS name encoding, observed pagination semantics, provider ID and response bounds, non-address row preservation, and CNAME cardinality conflicts.
- `source-publishing-scope`: stale-status incompleteness, listener hostname intersection with per-parent addresses, hostname validation, apex skipping, duration TTLs, and domain filter validation.
- `dns-policy-governance`: target-kind restrictions and namespace-scoped invalid policies with cleanup suppression.
- `multi-target-management`: per-target isolation, token trimming, timeout semantics, retries default, and in-zone domain filters.
- `shared-zone-ownership`: claim release after deletes and convergence of interrupted claim writes.
- `structured-plan-audit`: no plans for non-actionable cycles, stale superseded approvals, policy completeness, and terminal phases after timeouts.
- `controller-operability`: Lease release ordering, retry pacing, target-mode liveness, client-go logging, target list retry, and backoff under event churn.
- `reconciliation-status`: truthful conditions, history deduplication, target-mode reconcile metrics, and monitoring assets.
- `deployment-artifact-consistency`: chart writer guards, RBAC, egress, values, CRDs, CI workflow behavior, and toolchain pinning.
- `supply-chain-security`: release version guard, private vulnerability reporting, grouped Kubernetes updates, and toolchain pinning.

## Impact

Affected areas are `internal/fortigate`, `internal/controller`, `internal/plan`, `internal/workqueue`, `internal/status`, `internal/source`, `internal/policy`, `internal/ownership`, `internal/target`, `internal/config`, `cmd/fortigate-external-dns`, the Helm chart and raw manifests, CI and release workflows, and the READMEs. Upgrades need a dry-run review: rows written by v0.3.1 and earlier with FQDN hostnames or undotted CNAME targets are planned for replacement. CRDs must be applied before `helm upgrade`, and the policy CIDR validation requires Kubernetes 1.31 or later. Chart configurations with several replicas and no leader election now fail to render. Losing leadership now exits non-zero.
