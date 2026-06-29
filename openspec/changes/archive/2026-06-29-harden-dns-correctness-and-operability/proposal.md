## Why

A multi-dimensional code review of the controller confirmed 39 findings (2 high, 8 medium) that can produce **incorrect or duplicate DNS records** on the FortiGate, generate **endless write churn**, **silently break health/metrics serving**, or break documented features under specific configurations. These are correctness, data-safety, and operability gaps in shipping behavior — not new features — so they should be fixed before relying on the controller for production DNS.

## What Changes

Reconciliation & FortiGate client (data safety)
- Stop blindly retrying unkeyed `POST` creates so a lost-response retry can no longer create a duplicate `dns-entry` (HIGH).
- Detect and reconcile duplicate owned rows that currently collapse silently in the planner by `Key()`, so a duplicate can be cleaned up instead of persisting forever (MEDIUM).
- Make `deactivate` cleanup idempotent: skip already-disabled stale records instead of re-`PUT`ing them every loop (MEDIUM).
- Reject owner IDs that contain the comment delimiters `;`/`=` so the managed-comment round-trip cannot corrupt ownership metadata (MEDIUM).
- Include `Source` in record equality so a changed owning resource rewrites the stored comment; retry transient FortiGate error-envelopes; give apply operations an intentional safety ordering.

Source publishing (scope safety)
- Reject (skip with a warning event) hostnames that are not equal to or a subdomain of the configured FortiGate zone, so arbitrary annotations can no longer inject out-of-zone records (HIGH).
- Skip leading-wildcard hostnames (`*.example.com`) with a warning instead of attempting an unsupported `dns-entry` (MEDIUM).
- Prefer hostname-or-IP (not both) from LoadBalancer/Ingress status to avoid coexisting A and CNAME for one name; emit a diagnostic event for hostname-less HTTPRoutes.

Controller operability
- Validate `MetricsAddr` and `LeaderElectionID` at startup, and treat a probe/metrics bind failure as fatal instead of running blind with readiness already `true` (MEDIUM).
- Record real apply outcomes (`applied`/`failed`/`skipped`/`conflict`) on the operations metric instead of only `planned`, and bound `DefaultTTL`/`Retries` (MEDIUM).
- Compare provider case-insensitively; flip readiness to not-ready on shutdown.

Deployment artifacts & docs
- Generate Helm RBAC for `gatewayTargetNamespaces` in namespaced mode so the documented feature does not fail with `forbidden` (MEDIUM).
- Replace the paraphrased `LICENSE` with the verbatim Apache License 2.0 (MEDIUM).
- Scope leader-election lease `get`/`update` by `resourceNames`; ship an optional metrics `Service`/`NetworkPolicy`; document `--cleanup-policy`; harden `secret-scan.sh`.

Tests
- Add coverage for the create-retry no-duplicate guard, ownership-conflict no-HTTP guard, idempotent deactivate, owner-ID-with-space comment round-trip, and out-of-zone/wildcard rejection.

## Capabilities

### New Capabilities
<!-- None. All affected behavior is governed by existing capabilities. -->

### Modified Capabilities
- `reconciliation-data-safety`: add requirements for idempotent stale cleanup, retry-safe creates, duplicate owned-row reconciliation, owner-ID delimiter safety, and source-aware record equality.
- `source-publishing-scope`: add requirements that published hostnames must be within the configured zone, that wildcard hostnames are not published, and that status targets do not produce a coexisting A/CNAME.
- `controller-operability`: add requirements for startup validation of bind/lease config, fatal probe-server bind failure, and apply-outcome metrics.
- `deployment-artifact-consistency`: add requirements for namespaced-mode gateway-target RBAC, least-privilege lease scoping, verbatim license text, and documenting the cleanup policy.

## Impact

- Code: `internal/plan/plan.go`, `internal/dns/endpoint.go`, `internal/fortigate/client.go`, `internal/source/{options,service,ingress,gateway}.go`, `internal/config/config.go`, `internal/controller/runner.go`, `cmd/fortigate-external-dns/main.go`, `internal/metrics/metrics.go`.
- Artifacts: `charts/fortigate-external-dns/templates/rbac.yaml` (+ optional `service.yaml`/`networkpolicy.yaml`), `manifests/rbac.yaml`, `scripts/secret-scan.sh`, `LICENSE`, `README.md`, `README.ko.md`.
- Behavior: out-of-zone and wildcard hostnames stop being published (a behavior change for any deployment that relied on the previous unscoped behavior); a malformed `--metrics-addr`/`--leader-election-id` now fails startup instead of degrading silently. No FortiGate API surface or CLI flag is removed.
- No new third-party dependencies; no GitHub Actions workflow files added.
