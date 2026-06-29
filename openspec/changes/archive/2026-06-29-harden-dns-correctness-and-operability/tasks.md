## 1. Reconciliation & client data safety (HIGH/MEDIUM)

- [x] 1.1 Thread the HTTP method into `doJSON` retry logic in `internal/fortigate/client.go` so a create `POST` is not retried on 429/5xx/transport error; keep PUT/DELETE retryable
- [x] 1.2 Add a test asserting a retryable create failure does not result in a second `POST` (no duplicate), complementing the existing GET-only retry test
- [x] 1.3 In `internal/plan/plan.go`, skip emitting a deactivate operation when `currentEndpoint.Disabled` is already true (idempotent deactivate)
- [x] 1.4 Add a plan test asserting zero operations for an already-disabled stale record under `deactivate` policy
- [x] 1.5 Change the planner's current-state map to `map[string][]dns.Endpoint` and reconcile duplicate owned rows (keep one, clean extras under delete/deactivate, warn under keep)
- [x] 1.6 Add plan tests for two owned rows sharing a key under each cleanup policy
- [x] 1.7 Reject owner IDs containing `;` or `=` in `FortiGateConfig`/`Config.Validate` with a clear error
- [x] 1.8 Add a comment round-trip test through `endpointToRecord`/`managedComment` → `toEndpoint`/`ownerFromComment` for an owner ID containing a space, plus one legacy space-delimited case
- [x] 1.9 Include `Source` (kind/namespace/name) in `dns.Endpoint.EqualRecord` and add a test that a source-only change emits an update
- [x] 1.10 Retry FortiGate error-envelope responses (`checkEnvelope`) that report a transient (5xx-equivalent) `http_status`, consistent with the create-retry rule
- [x] 1.11 Replace lexical operation ordering in `plan.go` with an explicit safety-phase rank (in-place updates/replaces, then creates, then deletes/deactivates), keeping key as the deterministic tiebreaker

## 2. Source publishing scope (HIGH/MEDIUM)

- [x] 2.1 In `internal/source/options.go` `appendEndpointForHost`, skip hostnames not equal to or a subdomain of `opts.Zone`, emitting a warning event
- [x] 2.2 In the same chokepoint, skip leading-wildcard hostnames (`strings.HasPrefix(host, "*.")`) with a warning event
- [x] 2.3 Add source tests for out-of-zone rejection, in-zone acceptance, and wildcard rejection (Service/Ingress/Gateway)
- [x] 2.4 In `serviceTargets`/`ingressTargets`, prefer hostname-or-IP per status entry so a single name does not get both an A/AAAA and a CNAME; add a test
- [x] 2.5 Emit a diagnostic event when an accepted HTTPRoute has no `spec.hostnames`

## 3. Controller operability (MEDIUM)

- [x] 3.1 Validate `MetricsAddr` (host:port) in `Config.Validate` when non-empty
- [x] 3.2 Require a non-empty `LeaderElectionID` in `Config.Validate` when leader election is enabled and `--once` is not set
- [x] 3.3 Add upper bounds for `DefaultTTL` and FortiGate `Retries` in validation with clear errors
- [x] 3.4 Compare `Provider` case-insensitively (or normalize during `Load`)
- [x] 3.5 Bind the probe/metrics listener synchronously in `main.go` before `SetReady(true)`; treat a bind failure as a fatal startup error
- [x] 3.6 Flip readiness to not-ready on shutdown before tearing down the server
- [x] 3.7 Record per-operation apply outcomes (`applied`/`failed`/`skipped`/`conflict`) on `operations_total` from the apply summary, and fix the `RecordOperation` docstring/README wording
- [x] 3.8 Add config-validation tests for 3.1–3.4 and a metrics test asserting non-`planned` results are recorded
- [x] 3.9 Make `runWithLeaderElection` result handling deterministic (block on the result when leadership was acquired instead of a racy non-blocking read)

## 4. Deployment artifacts & documentation (MEDIUM/LOW)

- [x] 4.1 Generate Helm RBAC granting `get`/`list` on `gateways`/`httproutes` in each `gatewayTargetNamespaces` entry when `namespaces` is set and the gateway source is enabled
- [x] 4.2 Scope leader-election lease `get`/`update` by `resourceNames: [<lease>]` in `charts/.../templates/rbac.yaml` and `manifests/rbac.yaml` (keep `create` namespace-wide)
- [x] 4.3 Replace `LICENSE` with the verbatim official Apache License 2.0 text (including the appendix)
- [x] 4.4 Document `--cleanup-policy`/`CLEANUP_POLICY` (default `delete`, plus `deactivate`/`keep`) in `README.md` and `README.ko.md`, noting `delete` is destructive
- [x] 4.5 Harden `scripts/secret-scan.sh`: broaden the value char class to cover base64 Secret values, anchor placeholder exclusions to the matched value (not the whole line), and scan Secret `data`/`stringData`
- [x] 4.6 Add an optional metrics `Service` (and guarded `NetworkPolicy`/scrape annotations) gated behind a chart values flag so `/metrics` is scrapeable
- [x] 4.7 Add a startup warning when the `--fortigate-api-token` flag is used (token on the command line); add a note in `manifests/` that all namespace fields must change together
- [x] 4.8 Remove or wire up dead `Event.Severity`/`Event.Namespace` fields in `internal/source/options.go`

## 5. Tests & verification

- [x] 5.1 Add a client test that an `OperationConflict` is skipped by `Apply` with zero HTTP calls
- [x] 5.2 Run `make test`, `make static`, `make helm-template`, and `make validate`; confirm all pass (all pass; `make image` needs a Podman/Docker machine and is run separately)
- [x] 5.3 Run a `--dry-run --once` pass to confirm the plan output reflects the new scoping/idempotency behavior (covered locally by `make smoke`; a live device run needs a cluster)
- [x] 5.4 Update `docs/validation-results.md` with the new validation run
