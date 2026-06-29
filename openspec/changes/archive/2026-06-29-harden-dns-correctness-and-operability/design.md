## Context

The controller already separates concerns cleanly: `internal/source` discovers desired endpoints, `internal/plan` diffs desired vs. current, and `internal/fortigate` applies operations over the CMDB REST API. A code review confirmed the architecture is sound but found correctness and operability gaps in specific code paths. This design covers how to close those gaps without reshaping the architecture or changing the FortiGate API surface.

Two constraints shape the approach:
- A FortiGate `dns-entry` mkey is a server-assigned integer with no client-supplied key, so creates are inherently non-idempotent under retry.
- The owner ID and source identity are persisted only inside the record's free-text `comment`, which is the controller's sole ownership signal across restarts.

## Goals / Non-Goals

**Goals:**
- Eliminate paths that can create duplicate or out-of-zone records, or churn writes against already-correct records.
- Fail fast and loudly on misconfiguration that would otherwise degrade health/metrics silently.
- Keep the existing reconciliation model, CLI flags, and FortiGate request shapes intact.
- Cover each fixed behavior with a test that would fail on regression.

**Non-Goals:**
- No switch to Kubernetes informers/watches (list-based polling stays).
- No new DNS record types, providers, or external dependencies.
- No support for wildcard or ExternalName records (they are explicitly skipped with diagnostics, not implemented).
- No change to the leader-election protocol itself.

## Decisions

**1. Retry-safe creates: gate retries on HTTP method, not just status.**
Thread the request method into `doJSON`'s retry decision and treat a `POST` (create) failure on 429/5xx/transport-error as terminal, letting the next full reconcile converge. Chosen over a pre-`POST` "list and skip if exists" lookup because the lookup adds a round trip and its own race window; the next reconcile already re-lists current state and (with decision #3) can clean a duplicate if one slipped through. PUT/DELETE remain retryable since they are keyed and idempotent.

**2. Idempotent deactivate: guard on current state.**
In the `CleanupDeactivate` branch of `plan.Build`, skip emitting an operation when `currentEndpoint.Disabled` is already true. This mirrors the existing "no update when records are equal" logic and needs no new state.

**3. Duplicate owned rows: model current state as a multimap.**
Change the planner's `currentByKey` from `map[string]Endpoint` to `map[string][]Endpoint`. When more than one owned row shares a key, keep one as the match target and emit cleanup operations (delete/deactivate under the active policy) for the extras, or a warning under `keep`. This is the minimal change that makes the duplicate observable and reconcilable; it composes with decision #1 to self-heal a duplicate that a lost-response create produced.

**4. Owner-ID delimiter safety: validate, don't escape.**
Reject `;` and `=` in the owner ID at config validation. Chosen over percent-encoding the comment because validation is a one-line guard with no migration concern, the delimiters are not meaningful in a cluster identifier, and encoding would complicate the legacy-comment backward-compat path.

**5. Zone-scoped publishing: enforce in one chokepoint.**
Add the `host == zone || strings.HasSuffix(host, "."+zone)` check (and the leading-`*.` wildcard check) in `appendEndpointForHost`, the single function all sources funnel through, so every source inherits the guard and emits a consistent warning event. This is independent of `--domain-filter`, which stays as an additional narrowing knob.

**6. Fatal probe bind: bind synchronously before backgrounding Serve.**
`net.Listen` on the configured address in the main goroutine; on error, return it to `main` and exit non-zero. Only the blocking `Serve` runs in the goroutine. `SetReady(true)` moves to after a successful bind. Chosen over a best-effort error channel because a controller whose probes can't be served should not run.

**7. Apply-outcome metrics: record after Apply.**
Have the FortiGate client return per-operation outcomes (or have the runner record them from the apply summary it already computes) so `operations_total{result=...}` carries `applied`/`failed`/`skipped`/`conflict`, and fix the metric docstring.

## Risks / Trade-offs

- **Behavior change: out-of-zone/wildcard hostnames stop publishing.** → A deployment that (mis)relied on the previous unscoped behavior will see those records disappear on the next reconcile. Mitigation: emit explicit warning events naming each skipped hostname; call out the behavior change in the proposal and release notes.
- **Terminal create on retryable failure could briefly miss a record.** → A create that hit a transient 5xx is not retried within the loop. Mitigation: the next reconcile interval re-creates it; this is strictly safer than risking a duplicate, and convergence latency is bounded by the interval.
- **Multimap planner change touches the safety-critical diff.** → Risk of regressing the existing replace-pairing logic. Mitigation: keep the existing `Key()`/`LogicalKey()` semantics; add table tests for the duplicate case alongside the existing replace/conflict tests before refactoring.
- **Fatal bind failure changes crash semantics.** → A transient port conflict now exits instead of degrading. Mitigation: this is the intended fail-loud behavior; Kubernetes restart/backoff handles transient conflicts and surfaces a clear error.

## Migration Plan

1. Land planner and client correctness fixes (decisions 1–4) with tests; these are behavior-preserving for in-zone, non-duplicate steady state.
2. Land zone/wildcard scoping (decision 5) — the one operator-visible behavior change — with warning events and doc/release-note callouts.
3. Land operability fixes (decisions 6–7) and config validation.
4. Land deployment-artifact and documentation fixes (RBAC, lease scoping, LICENSE, cleanup-policy docs, secret-scan).
5. Rollback is per-commit and low-risk: each fix is independent; reverting the scoping commit restores prior publishing behavior if an operator depended on it.

## Open Questions

- For duplicate owned rows under `keep`, is a warning sufficient, or should the controller pick a deterministic survivor to report? (Default: warning only, since `keep` means "do not mutate".)
- Should the optional metrics `Service`/`ServiceMonitor`/`NetworkPolicy` ship enabled-by-default or gated behind a values flag? (Leaning: gated, default off, to avoid surprising existing installs.)
