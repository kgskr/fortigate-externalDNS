## Why

The controller now has strong fail-closed reconciliation for one exclusively managed FortiGate DNS database, but that operating model prevents safe coexistence with manual records, limits namespace-scoped cleanup, provides only log-oriented change review, and requires one deployment per target. The next release should turn those constraints into explicit platform capabilities while preserving FortiGate-only scope and the current safety invariants.

## What Changes

- Add a deterministic, machine-readable reconciliation plan with a content hash, durable audit result, and an optional approval gate before mutation.
- Add a CRD-backed ownership registry so records can be safely adopted, updated, and cleaned in a shared FortiGate DNS database without trusting undocumented FortiOS fields.
- Add namespace-aware DNS policy controls for hostname suffixes, TTL bounds, target CIDRs, source kinds, record quotas, and explicit publication opt-in.
- Add Kubernetes status resources and expanded Prometheus metrics for drift, conflicts, incomplete discovery, provider snapshot age, plans, and applies.
- Replace timer-only reconciliation with a rate-limited informer workqueue while retaining periodic full-provider audits as the cleanup safety boundary.
- Add multiple independently isolated FortiGate zone/VDOM targets to one controller process, with per-target credentials, ownership, status, queues, and failure isolation.
- Add opt-in `ExternalName` Service and headless Service/EndpointSlice publication; continue to reject NodePort and wildcard publication until a separate supported design exists.
- Add Cosign signing, SPDX SBOM generation, and SLSA-compatible provenance for published images and Helm artifacts.
- Keep all new runtime behavior disabled or compatibility-preserving by default; existing single-target exclusive-zone installations continue to work unchanged.

## Capabilities

### New Capabilities

- `structured-plan-audit`: Defines deterministic JSON plans, plan hashes, approval gates, and durable apply audit results.
- `shared-zone-ownership`: Defines the CRD ownership registry, adoption, migration, optimistic concurrency, orphan handling, and shared-zone cleanup rules.
- `dns-policy-governance`: Defines namespace-aware publication policy, limits, precedence, rejection diagnostics, and safe defaults.
- `reconciliation-status`: Defines Kubernetes status resources, conditions, bounded history, metrics, and operator-facing diagnostics.
- `multi-target-management`: Defines multiple FortiGate targets, Secret references, routing, isolation, and per-target health.

### Modified Capabilities

- `controller-operability`: Add informer/workqueue-driven reconciliation, periodic full audits, rate limiting, coalescing, and per-target failure isolation.
- `reconciliation-data-safety`: Make approved plan identity, ownership-registry state, and full-audit freshness prerequisites for destructive mutations.
- `source-publishing-scope`: Add opt-in ExternalName and headless Service/EndpointSlice publication with explicit target and conflict semantics.
- `deployment-artifact-consistency`: Package CRDs, RBAC, target/policy/status configuration, dashboards, and compatibility-safe Helm values.
- `supply-chain-security`: Require signed release artifacts, SPDX SBOMs, and verifiable provenance.

## Impact

This affects the reconciliation pipeline, Kubernetes clients and informers, planner and apply contracts, FortiGate target/client construction, new CRD APIs and generated manifests, controller RBAC, Helm values/schema/templates, metrics and status reporting, release workflows, tests, samples, and operator documentation. New Kubernetes API dependencies are required for CRD types, dynamic ownership/status persistence, EndpointSlice discovery, and workqueue processing; release jobs gain Cosign and provenance tooling. Existing flags and single-target values remain supported during the migration period.
