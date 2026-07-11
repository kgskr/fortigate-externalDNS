## 1. API foundations and compatibility contracts

- [x] 1.1 Add `internal/apis/v1alpha1` Go types for `FortiGateDNSTarget`, `FortiGateDNSRecordOwnership`, `FortiGateDNSPolicy`, `FortiGateDNSChangePlan`, and `FortiGateDNSStatus`, including condition/reason enums and Secret/CA key references.
- [x] 1.2 Add structural CRD manifests with status subresources, list/map semantics, bounded fields, immutable identity fields where applicable, and printer columns that expose only sanitized state.
- [x] 1.3 Add CRD schema tests that reject invalid modes, unbounded retention, malformed references, unsupported reasons, and secret-bearing fields.
- [x] 1.4 Add a dynamic-client conversion layer that round-trips typed API objects without losing resourceVersion, generation, status, or finalizers.
- [x] 1.5 Add compatibility tests proving existing CLI/environment configuration synthesizes one unchanged `default` target and conflicts with explicit CRD target mode.

## 2. Canonical plan and audit contract

- [x] 2.1 Extend the plan model with a versioned target-scoped document, provider/discovery/policy/ownership preconditions, prerequisite edges, safety decisions, and sanitized operation representations.
- [x] 2.2 Implement canonical JSON serialization with deterministic sorting and SHA-256 identifiers; add order-randomized golden tests and secret-exclusion tests.
- [x] 2.3 Change apply to consume and revalidate a plan document before and during mutation while preserving independent-operation progress and dependent-cleanup blocking.
- [x] 2.4 Add one-shot `--plan-output` and `--approved-plan-hash` configuration with atomic file output, explicit overwrite behavior, and validation that approval mode never falls back to implicit apply.
- [x] 2.5 Implement the long-running `FortiGateDNSChangePlan` store, exact approval-hash annotation handling, stale-plan replacement, terminal phases, per-operation outcome summaries, and bounded retention.
- [x] 2.6 Add tests for missing/mismatched approval, provider revision drift, policy/claim resourceVersion drift, interrupted apply, partial independent outcomes, and retention that preserves pending plans.

## 3. Shared-zone ownership registry

- [x] 3.1 Implement normalized ownership keys and fingerprints that cover target, DNS name, record type, target value, TTL, and status without including secrets or undocumented FortiOS fields.
- [x] 3.2 Implement the ownership repository with optimistic resourceVersion writes, finalizers, Reserved/Confirmed/Orphaned/Conflict phases, and duplicate-claim detection.
- [x] 3.3 Reserve claims before creates and confirm them only after a stable provider relist observes exactly one matching record and provider ID.
- [x] 3.4 Reconcile lost create responses without duplicate provider rows and prevent Reserved or Orphaned claims from authorizing destructive cleanup.
- [x] 3.5 Require exact Confirmed claim preconditions for shared-zone update, replace, deactivate, and delete operations and revalidate them immediately before each request.
- [x] 3.6 Implement explicit exact-match adoption plans, approval, provider-revision checks, and conflict behavior for changed or already-claimed candidates.
- [x] 3.7 Add shared-zone tests for concurrent claim conflicts, deleted claims, provider ID reuse, duplicate rows, fingerprint drift, adoption races, orphan recovery, and exclusive-mode compatibility.

## 4. DNS policy governance

- [x] 4.1 Implement namespaced policy listing, selectors, source-kind filters, hostname/target constraints, TTL bounds, quotas, and opt-in annotation fields.
- [x] 4.2 Implement deterministic restrictive-intersection evaluation with deny precedence and global/target configuration as non-widenable outer bounds.
- [x] 4.3 Insert policy evaluation after endpoint normalization/conflict detection and before planning; return fixed rejection reasons and source events without leaking arbitrary values into metric labels.
- [x] 4.4 Mark policy list/parse/evaluation-state failures incomplete so cleanup is suppressed, while treating intentional policy denial as a complete desired-state decision.
- [x] 4.5 Implement deterministic namespace/target quota accounting and stable excess selection independent of informer/list order.
- [x] 4.6 Add policy unit and integration tests for overlapping intersections, deny precedence, empty intersections, required opt-in, target CIDRs, hostname targets, quotas, disabled-mode compatibility, and policy API failure.

## 5. Kubernetes source expansion

- [x] 5.1 Add feature configuration and annotation/policy gates for ExternalName and headless Service publication; keep both disabled by default.
- [x] 5.2 Implement ExternalName CNAME discovery with DNS normalization, IP/malformed-target rejection, TTL/domain/policy enforcement, and existing CNAME conflict semantics.
- [x] 5.3 Add EndpointSlice clients and informer data to Kubernetes discovery and map slices by the standard Service name label.
- [x] 5.4 Implement headless A/AAAA discovery with ready/readiness-unknown filtering, `publishNotReadyAddresses`, address-family validation, deterministic deduplication, and hostname-over-IP precedence.
- [x] 5.5 Mark EndpointSlice cache/list failures incomplete and suppress affected cleanup while allowing independently proven non-destructive operations.
- [x] 5.6 Add tests for ExternalName, dual-stack EndpointSlices, terminating/unready/unknown endpoints, publish-not-ready behavior, slice deletion, missing API, duplicate addresses, and explicitly unsupported NodePort/ClusterIP/wildcard/SRV modes.

## 6. Event-driven reconciliation engine

- [x] 6.1 Add shared informers for enabled sources, EndpointSlices, targets, policies, ownership claims, change plans, and referenced Secret metadata, and require all relevant caches to synchronize before mutation.
- [x] 6.2 Implement event-to-target mapping with semantic update filters so relevant add/update/delete events enqueue every affected target and status-only noise is ignored.
- [x] 6.3 Add a typed rate-limited target workqueue with per-target coalescing, configurable debounce, capped exponential backoff with jitter, and one in-flight worker per target key.
- [x] 6.4 Retain periodic resync by enqueueing every target and require every cleanup-capable run to perform complete cached discovery plus a stable FortiGate snapshot.
- [x] 6.5 Integrate leadership lifecycle so loss/cancellation stops new mutation, cancels in-flight work, shuts informers down cleanly, and leaves future convergence to the next leader.
- [x] 6.6 Add deterministic fake-clock/workqueue tests for event latency, update filtering, storms, retry reset/exhaustion, periodic drift detection, cache-sync failure, target deletion, and leadership loss during apply.

## 7. Multi-target runtime and isolation

- [x] 7.1 Implement target loading/validation, normalized domain-overlap detection, explicit non-destructive overlap acknowledgement, and legacy default-target synthesis.
- [x] 7.2 Resolve API-token Secret keys and optional CA ConfigMap/Secret keys in memory with namespaced least-privilege reads, rotation-triggered enqueueing, and sanitized errors.
- [x] 7.3 Construct independent FortiGate clients, plan/ownership/status stores, queue state, retry/circuit state, and metrics context per target.
- [x] 7.4 Route normalized desired endpoints to exactly the eligible target set and reject ambiguous write routing before provider mutation.
- [x] 7.5 Ensure one target's provider, credential, policy, ownership, approval, or retry failure never blocks another target's worker.
- [x] 7.6 Add multi-target tests for independent zones/VDOMs, overlapping suffixes, Secret absence/rotation, CA rotation, failing/healthy concurrency, target deletion, and legacy mutual exclusion.

## 8. Status, metrics, and operator diagnostics

- [x] 8.1 Implement one `FortiGateDNSStatus` writer per target with the seven specified conditions, observed generations/revisions, bounded counts, plan hash, timestamps, and conflict-safe status updates.
- [x] 8.2 Implement bounded audit summary history and fixed reason/message sanitization; add tests proving tokens, response bodies, hostnames, provider IDs, and full records cannot enter status.
- [x] 8.3 Extend Prometheus metrics with bounded target readiness, desired/current/drift/conflict counts, incomplete source state, provider snapshot age, queue depth/retries, plan phases, and apply outcomes.
- [x] 8.4 Add metric cardinality tests that feed arbitrary hostnames, resource names, provider IDs, and errors and assert they never become labels.
- [x] 8.5 Add optional Grafana dashboard and PrometheusRule-compatible alert templates for stale reconcile, provider unreachable, ownership conflict, pending approval, incomplete discovery, and cleanup refusal.

## 9. Controller integration and configuration

- [x] 9.1 Wire API clients, informers, target manager, policy evaluator, plan store, ownership store, workers, status, and metrics through explicit interfaces without global mutable state.
- [x] 9.2 Add CLI/environment/Helm configuration for target mode, plan output/approval, shared ownership, policy enforcement, debounce/resync, source expansion, status retention, and monitoring assets with fail-closed validation.
- [x] 9.3 Preserve `--once` semantics as one complete target audit, deterministic plan output, optional exact-hash apply, and nonzero exit on blocked/stale/failed plans.
- [x] 9.4 Preserve dry-run semantics across exclusive/shared and single/multi-target modes without creating provider records or falsely Confirming ownership claims.
- [x] 9.5 Add end-to-end fake Kubernetes/FortiGate tests covering exclusive legacy mode, shared adoption and mutation, approved and rejected plans, policy removal with cleanup guards, event-driven convergence, and two isolated targets.
- [x] 9.6 Run race-focused tests for concurrent target events, claim changes, plan approval, Secret rotation, status writes, and leadership cancellation.

## 10. Helm, RBAC, manifests, and validation scripts

- [x] 10.1 Package all CRDs under the chart and repository manifests with structural schemas, status subresources, storage version, categories, short names, and sanitized printer columns.
- [x] 10.2 Extend Helm values, JSON schema, deployment arguments, volumes, checksums, ServiceAccount, RBAC, NOTES, and examples for every new opt-in capability while preserving legacy defaults.
- [x] 10.3 Add least-privilege RBAC for EndpointSlices and controller CRDs, target Secret/CA references, status updates, and finalizers; document the exact grants required when `rbac.create=false`.
- [x] 10.4 Update raw manifests for the supported legacy path and provide separate example manifests for CRD target/shared-zone mode without embedding credentials.
- [x] 10.5 Extend Helm validation scripts to render default, legacy, shared, approval, policy, multi-target, source-expansion, monitoring, and invalid combination cases and assert safety-critical fields.
- [x] 10.6 Add CRD structural-schema, RBAC, dashboard/rule, YAML parsing, secret-scan, and code-to-schema drift checks to Makefile and CI.

## 11. Release signing, SBOM, and provenance

- [x] 11.1 Pin trusted Cosign and SBOM tooling by version/digest and grant GitHub OIDC permission only to the release-published job.
- [x] 11.2 Generate and attach SPDX JSON SBOMs for the final image digest and packaged Helm chart, and fail release if either artifact is missing.
- [x] 11.3 Generate SLSA-compatible provenance for image and chart artifacts from immutable source commit and workflow identity.
- [x] 11.4 Keyless-sign the image digest and chart archive with expected repository/workflow identity and issuer constraints; never sign mutable tags as evidence.
- [x] 11.5 Add non-publishing CI validation for workflow permissions, pinned actions/tools, expected attestations, and fork/PR isolation.
- [x] 11.6 Add a verification script and documented Cosign/SBOM/provenance commands, including negative tests for modified bytes, wrong digest, identity, issuer, and repository.

## 12. Documentation and migration

- [x] 12.1 Update English and Korean README architecture, feature matrix, configuration, metrics, safety invariants, and troubleshooting for all new capabilities.
- [x] 12.2 Document exclusive-to-shared migration: dry-run, adoption candidate review, plan approval, Confirmed-claim gate, write enablement, backup, rollback, and old-controller prohibition.
- [x] 12.3 Document legacy-to-multi-target migration, overlap validation, Secret/CA rotation, target failure isolation, and one-deployment-per-target alternative.
- [x] 12.4 Add samples for policies, targets, ownership adoption, plan approval, ExternalName, headless dual-stack Service/EndpointSlices, monitoring assets, and release verification.
- [x] 12.5 Update chart README, NOTES, raw-manifest guidance, decommissioning, disaster recovery for CRD loss, and bounded status/audit retention.
- [x] 12.6 Update baseline OpenSpec specs and `docs/validation-results.md` only after implementation evidence proves each new requirement.

## 13. Completion verification

- [x] 13.1 Run `gofmt`, full unit/integration tests, `go test -race ./...`, `go vet ./...`, vulnerability scanning, and module verification with no failures.
- [x] 13.2 Run format, static, Helm render, CRD schema, RBAC, YAML, secret-scan, release-workflow, and artifact-verification checks from the repository Makefile/CI commands.
- [x] 13.3 Run `openspec validate --all --strict` and prove every scenario has a corresponding automated test or documented external release verification gate.
- [x] 13.4 Perform a requirement-by-requirement evidence audit covering compatibility defaults, fail-closed cleanup, secret exclusion, bounded status/metrics, target isolation, shared ownership, and release trust evidence.
