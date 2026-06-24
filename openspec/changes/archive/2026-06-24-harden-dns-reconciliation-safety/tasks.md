## 1. Safety Regression Tests

- [x] 1.1 Add planner tests for target replacement so `A 1.1.1.1 -> A 2.2.2.2` is not emitted as unsafe unordered create plus delete.
- [x] 1.2 Add planner tests proving multiple A/AAAA targets for the same hostname remain distinct.
- [x] 1.3 Add FortiGate apply tests proving one failed operation does not block later independent operations.
- [x] 1.4 Add config tests proving malformed non-empty env values such as `DRY_RUN=ture` fail loading.
- [x] 1.5 Add FortiGate response fixture tests for HTTP 2xx error envelopes.
- [x] 1.6 Add DNS normalization tests proving caller target slices are not mutated.

## 2. Reconciliation Data Safety

- [x] 2.1 Refactor endpoint identity so logical DNS records, FortiGate entry IDs, and target-per-entry identity are explicit instead of overloaded into one `Key()`.
- [x] 2.2 Implement replacement-aware planning for same zone/name/type target changes.
- [x] 2.3 Implement replacement execution preferring provider-ID based PUT in place for 1:1 same zone/name/type replacements, falling back to delete-before-create only when no provider ID is available; cover ordering and fallback in tests.
- [x] 2.4 Change FortiGate apply to continue independent operations, aggregate errors, and log attempted/succeeded/failed/skipped/conflict counts.
- [x] 2.5 Remove DNSName fallback for PUT/DELETE record identifiers and return explicit missing-provider-ID errors.
- [x] 2.6 Validate FortiGate response envelopes for `status`, `http_status`, `error`, and `message` fields even on HTTP 2xx responses.
- [x] 2.7 Make retry backoff context-aware and preserve request body replay behavior across retries.
- [x] 2.8 Make endpoint normalization copy target slices before sorting or normalizing.
- [x] 2.9 Replace fragile whitespace-based owner/source comment parsing with a more robust parser or encoded metadata format.

## 3. Strict Configuration

- [x] 3.1 Refactor env parsing helpers to return errors for malformed non-empty bool, int, and duration values.
- [x] 3.2 Update `Load()` to aggregate and report configuration parsing errors with variable names.
- [x] 3.3 Preserve and report meaningful kubeconfig and in-cluster configuration errors instead of hiding the first failure.
- [x] 3.4 Update README and Helm values documentation for strict parsing and safe dry-run configuration.

## 4. Controller Operability

- [x] 4.1 Add reconcile timeout configuration and wrap each `RunOnce` reconciliation in `context.WithTimeout`.
- [x] 4.2 Add Kubernetes Lease-based leader election with default-on in-cluster behavior and local-test override flags; `--once` runs bypass leader election.
- [x] 4.3 Add health/readiness HTTP server with `/healthz` and `/readyz` endpoints.
- [x] 4.4 Add `/metrics` (prefix `fortigate_external_dns_`) exposing reconcile total/error counters, a reconcile duration histogram, applied-operation counters by type and result, and a last-successful-reconcile gauge, without sensitive values.
- [x] 4.5 Add unit tests for reconcile timeout, non-leader no-op behavior, health/readiness, and metrics output.
- [x] 4.6 Update Helm chart and raw manifests with leader election, probe, metrics port, and required Lease RBAC settings.

## 5. Source Publishing Scope

- [x] 5.1 Add explicit `gateway-target-namespace` configuration separate from source namespaces and cleanup scope.
- [x] 5.2 Update Gateway discovery to read configured target namespaces for parent Gateway address lookup.
- [x] 5.3 Ensure target lookup namespaces do not expand stale record cleanup scope.
- [x] 5.4 Keep full Gateway API parentRef identity matching for group, kind, namespace, name, sectionName, and port.
- [x] 5.5 Emit explicit warning events/logs for ClusterIP, headless, NodePort, and other unsupported Service types; add no new publishing modes in this change.
- [x] 5.6 Add tests for shared infra Gateway lookup, missing target namespace reporting, cleanup scope separation, and unsupported Service type events.

## 6. Deployment and Documentation Consistency

- [x] 6.1 Align raw manifests with Helm hardening defaults or clearly mark them as minimal examples.
- [x] 6.2 Remove unused `watch` RBAC verbs unless watch behavior is implemented.
- [x] 6.3 Update README references that drifted from the actual Containerfile and runtime behavior.
- [x] 6.4 Keep `.github/workflows` absent and add a validation check proving no workflow files were introduced.
- [x] 6.5 Remove or wire up unused helpers such as redaction, sorting, and mutable-operation helpers.
- [x] 6.6 Update validation docs with local commands for tests, vet, Helm rendering, manifest checks, container build, and secret/workflow scans.
- [x] 6.7 Add restricted-PSS hardening to the Helm chart (`seccompProfile: RuntimeDefault`, `readOnlyRootFilesystem: true`, default resource requests/limits) and pin the controller image by digest or a documented tag policy; reflect the same posture or a minimal-example note in raw manifests.

## 7. Validation and Review

- [x] 7.1 Run `go test ./...` with a repo-local `GOCACHE`.
- [x] 7.2 Run `go vet ./...` with a repo-local `GOCACHE`.
- [x] 7.3 Run Helm lint/template validation, including namespace-scoped and leader-election-enabled values.
- [x] 7.4 Run local manifest/RBAC checks proving no unused watch verbs and required Lease permissions.
- [x] 7.5 Run secret scan and `.github/workflows` absence check.
- [x] 7.6 Run or document container image build validation.
- [x] 7.7 Run a subagent review focused on DNS data safety and deployment safety before commit.
