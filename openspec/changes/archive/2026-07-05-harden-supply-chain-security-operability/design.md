# Design: harden-supply-chain-security-operability

## Context

The 2026-07-04 project review confirmed strong code-level security (token
handling, injection containment, ownership scoping) but found three structural
gaps:

1. **Supply chain**: base images and actions are tracked by mutable tags, no
   vulnerability scanning exists anywhere, and CI never builds the container
   image — the `go.mod` 1.26 / `golang:1.25` builder drift reached `main`
   undetected and silently blocked the `v0.1.1` security release.
2. **Security depth**: FortiGate management interfaces usually present
   private-CA certificates, and the only TLS knobs are "system CAs" or
   `--fortigate-insecure-skip-verify`, so real deployments are pushed toward
   disabling verification for a firewall-admin credential. There is no egress
   containment and no guard against a successfully-empty discovery planning
   deletion of every owned record.
3. **Operability**: chart probes disappear when `metrics.enabled=false`;
   liveness never reflects a wedged reconcile loop; the chart's `dryRun: true`
   default is undocumented in install steps; there is no values schema, chart
   README, or NOTES.txt; token rotation has no documented restart path; logs
   are text-only and the binary carries no version identity.

Current mechanics that constrain the design: the probe/metrics endpoints
(`/healthz`, `/readyz`, `/metrics`) are served by one HTTP server bound to
`metrics-addr` (`internal/serve`); leader election means non-leader replicas do
not reconcile; release publishing triggers on GitHub Release published and
gates on the reusable CI workflow (`workflow_call`); the chart never creates
the token Secret (`existingSecret` only); config parsing fails closed on
malformed typed values.

## Goals / Non-Goals

**Goals:**

- Every build input (base images, actions) is immutable-pinned and update-tracked.
- A vulnerability in the Go module graph, Go toolchain/stdlib, or runtime base
  image produces an automated, visible signal — before release (CI) and after
  release (weekly rescan of published artifacts).
- CI builds the container image on every PR so builder/toolchain drift can
  never again reach `main` unnoticed.
- Private-CA FortiGate deployments can verify TLS without disabling verification.
- A misconfiguration that empties the desired record set cannot mass-delete
  owned records in one cycle without explicit operator intent.
- Probes, liveness semantics, chart documentation/schema, token rotation, log
  format, and version identity reach day-2 production quality.

**Non-Goals:**

- Image/chart signing and SLSA provenance (cosign) — worthwhile, but a separate
  change; this one establishes detection, not attestation.
- Bridging client-go/klog output into slog.
- Leader-election, source-discovery, or planner-semantics changes beyond the
  deletion guard.
- Renovate migration; Dependabot is already adopted and sufficient.
- Restructuring `manifests/`; they only stay aligned per the existing
  deployment-artifact-consistency spec.

## Decisions

### D1. Pinning: digests for images, SHAs for actions, Dependabot keeps both fresh

`Containerfile` pins `golang:1.26-bookworm@sha256:<manifest-list-digest>` and
`gcr.io/distroless/static-debian12:nonroot@sha256:<manifest-list-digest>`; the
tag stays in the reference as human-readable context. The digest MUST be the
multi-arch manifest-list digest so `--platform=$BUILDPLATFORM` and
cross-compilation keep working. Workflow steps pin `owner/action@<40-char-sha>`
with a trailing `# vN.n.n` comment. Dependabot's `docker` and `github-actions`
ecosystems both natively update digest/SHA pins while preserving the comment.

*Alternative considered*: tag-only pinning plus trust in registry immutability —
rejected; tags are mutable references and this is exactly the class the review
flagged (release workflow holds `packages: write`).

### D2. Two scanners, each where it is strongest

- **govulncheck** (official Go vulndb, call-graph aware) runs in CI validation
  against source; it catches Go module and stdlib vulnerabilities that actually
  affect reachable code and has near-zero false-positive noise.
- **Trivy** scans the *built image* in CI; it catches what govulncheck cannot:
  distroless OS packages, the CA bundle, and binary-embedded module versions.
  CI builds the image with `docker/build-push-action` (`push: false`,
  host-native single arch for speed) — this build step doubles as the
  builder-drift gate that was missing. Trivy fails on `HIGH,CRITICAL` with
  `--ignore-unfixed` so unfixable upstream noise cannot brick CI; a tracked
  `.trivyignore` is the documented escape hatch for accepted findings.

Both steps live in the reusable CI workflow so `workflow_call` reuse keeps
gating releases on the same checks. Multi-arch remains release-only.

*Alternative considered*: grype instead of Trivy — comparable; Trivy chosen for
the maintained official action and wider ecosystem familiarity. Running only
image scanning without govulncheck — rejected; image scanners see module
*versions*, not reachability, and lag the Go vulndb.

### D3. Weekly scheduled rescan with issue-creation as the loud-failure channel

New `.github/workflows/scheduled-scan.yml`, `schedule:` cron (weekly) plus
`workflow_dispatch`. Jobs: (a) govulncheck against the default branch; (b)
Trivy against the latest published release image
(`ghcr.io/<owner>/fortigate-external-dns:latest`). On any finding the workflow
fails **and** creates-or-updates a labeled GitHub issue (`security-scan`) via
`gh` and the built-in `GITHUB_TOKEN`, deduplicating against an existing open
issue. Rationale: a red scheduled run on a quiet repo is effectively silent
(the review watched a broken release pipeline go unnoticed); an issue is
visible, assignable, and closeable. Workflow permissions: `contents: read`,
`issues: write` only.

*Alternative considered*: relying on workflow-failure email — rejected as the
silent-failure mode this change exists to eliminate. Auto-rebuild/republish of
patched images — rejected for now; publishing stays a deliberate,
release-gated act, the scheduled job's contract is *signal*, not *ship*.

### D4. CA trust: file-path flag, chart-owned ConfigMap, fail-closed conflict rule

New `--fortigate-ca-file` / `FORTIGATE_CA_FILE` takes a PEM bundle path, loaded
into `tls.Config.RootCAs` (replacing, not appending to, system roots — the
device is the only peer this client talks to). `tls.Config.MinVersion` is set
to TLS 1.2 unconditionally. Config validation errors when both `ca-file` and
`insecure-skip-verify` are set: the combination is contradictory trust intent,
and the project's established posture is fail-closed (strict env parsing
precedent). Empty/unreadable/non-PEM file is a startup validation error.

Chart: `fortigate.caBundle` (inline PEM string). When non-empty the chart
renders a ConfigMap, mounts it read-only at
`/etc/fortigate-external-dns/ca/ca.crt`, and passes the flag. Public CA
certificates are not secret, so a ConfigMap (visible, diffable) is preferred
over a Secret; operators with an existing CA Secret can mount it via the new
`extraVolumes`/`extraVolumeMounts` passthroughs if needed later — deliberately
not multiplied into `existingCAConfigMap`/`existingCASecret` variants now.

### D5. Mass-deletion guard: default-on empty-desired refusal + optional numeric cap

Two-layer guard over cleanup operations (`delete` *and* `deactivate` policies —
both take records out of service; `keep` plans nothing):

1. **Empty-desired refusal (default on)**: if discovery succeeds but the
   desired set for the managed scope is empty while current owned records
   exist, all cleanup operations that cycle are refused. This is the
   high-confidence misconfiguration signature (wrong domain-filter, wrong
   namespace, empty source list) and needs no tuning, so it can default on
   without a breaking change. Override: `--allow-empty-desired-cleanup` for
   intentional decommissioning (documented with `--once`).
2. **Numeric cap (opt-in)**: `--max-cleanup-per-cycle=N` (default `0` =
   unlimited) refuses the cycle's cleanup when planned cleanup operations
   exceed N. Opt-in because any default number is wrong for someone
   (50 records is "everything" in a small zone, noise in a large one).

Refusal is partial, not total: creates and updates still apply (they are the
safe direction), refused cleanup is logged at error level with the planned
count and surfaced via a new counter metric
(`fortigate_external_dns_cleanup_refused_total`). Next cycle re-evaluates
fresh — if the emptiness was a transient source outage, cleanup resumes
normally once discovery recovers.

*Alternative considered*: percentage-based threshold — rejected; ill-defined at
small record counts, and the empty-desired case covers the real incident
signature. Failing the whole reconcile — rejected; blocking creates/updates
prolongs an outage the guard did not prevent.

### D6. Liveness heartbeat: attempt-based, leader-scoped

`/healthz` fails when the controller *should* be reconciling but is not: it
returns non-success only when the replica currently holds leadership (or runs
with leader election disabled) **and** no reconcile attempt has *completed*
(success or failure) within the staleness window. `--healthz-max-staleness`
(duration, default `0` = auto: `max(5×interval, 5m)`) controls the window.

Key semantics, chosen to avoid restart storms:

- **Attempt-based, not success-based**: a FortiGate outage keeps the loop
  attempting and failing — that pod is healthy (a restart fixes nothing and
  would churn leadership). Success staleness remains the alerting concern via
  the existing `last_successful_reconcile_timestamp_seconds` metric. The
  heartbeat catches the wedged case: a loop that stopped completing attempts
  (deadlocked ticker, hung apply past its timeout, stuck client).
- **Leader-scoped**: non-leaders do not reconcile by design and MUST stay live;
  the heartbeat arms only while leading. Readiness semantics are unchanged.
- The runner records an attempt-completion timestamp (monotonic clock) that the
  probe handler reads; the window auto-derives from `interval` +
  `reconcile-timeout` so default deployments need no tuning.

Chart wires liveness `periodSeconds`/`failureThreshold` compatible with the
default window; raw manifests mirror it.

### D7. Probes always rendered; `metrics.enabled` gates exposure only

The binary already serves probes and metrics from one always-on server (bind
failure is fatal per existing spec). The chart bug is conflation: the
container port and both probes are templated inside the `metrics.enabled`
block. Fix in the chart, not the binary: always render the container port,
liveness, and readiness probes; `metrics.enabled` gates only the metrics
Service (scrape exposure) and its NetworkPolicy. `/metrics` remains reachable
pod-locally when the Service is off — acceptable because the metrics endpoint
is spec-guaranteed secret-free.

*Alternative considered*: separate probe listener/port in the binary — more
surface and a second bind-failure mode for no benefit.

### D8. Version identity via ldflags; log shape via flags

- `Makefile` and `Containerfile` stamp `-X main.version=<v> -X main.commit=<sha>`
  (defaults `dev`/`unknown`); release workflow passes them as build args from
  the release tag. `--version` prints `fortigate-external-dns <version> (<commit>)`
  and exits 0 before any config validation. A `build_info` gauge
  (value 1, labels `version`, `commit`) joins the metrics endpoint so running
  pods are correlatable to code without image-tag archaeology.
- `--log-format=text|json` selects `slog.NewTextHandler`/`NewJSONHandler`;
  `--log-level=debug|info|warn|error` sets the level. Both parse strictly
  (invalid value = startup error, per strict-parsing precedent). Defaults
  `text`/`info` preserve current output exactly.

### D9. Chart contract: schema, README, NOTES.txt, rotation path

- `values.schema.json` validates types, enums (e.g. `cleanupPolicy`), and
  required shapes for every documented value; `helm lint` + the existing
  template-check script enforce it in CI.
- Chart `README.md` documents every value with default and purpose
  (helm-docs table layout, maintained by hand — no new tooling dependency).
- `NOTES.txt` prints post-install state: whether `dryRun` is active (the #1
  new-user trap), the metrics Service state, and the token-Secret name in use.
- Token rotation: with `existingSecret` the chart cannot checksum-roll the pod,
  so the README and `values.yaml` comments document
  `kubectl rollout restart deployment/<release>` after rotating the Secret,
  and `podAnnotations` passthrough (existing) is called out for
  reloader-style controllers.
- Both top-level READMEs' install sections state the `dryRun: true` default and
  show the `--set dryRun=false` step.

## Risks / Trade-offs

- **[Digest pins go stale between Dependabot runs]** → Weekly cadence bounds
  the window; the scheduled rescan flags a vulnerable published image
  independently of whether a bump PR exists yet.
- **[Trivy false positives block CI]** → `--ignore-unfixed` plus a tracked,
  reviewed `.trivyignore`; scan step documented in validation docs so a
  maintainer can reproduce locally.
- **[CI image build lengthens PR feedback]** → Single-arch, cached
  (`actions/cache`-backed buildx layer cache) build; measured cost is minutes
  and it buys the drift gate that was the root cause of the broken release.
- **[Empty-desired refusal blocks intentional full teardown]** → Explicit
  `--allow-empty-desired-cleanup` override, documented next to
  `--cleanup-policy` with a `--once` decommissioning recipe.
- **[Heartbeat liveness kills a pod during a *very* slow but progressing
  apply]** → Window floor of `max(5×interval, 5m)` sits far above
  `reconcile-timeout`, which already bounds a single loop; an attempt that
  hits the timeout still *completes* and beats the heartbeat.
- **[Issue-creation spam from a flapping scheduled scan]** → Dedup against an
  open labeled issue; one issue accumulates run links until closed.
- **[values.schema.json rejects previously-tolerated sloppy values]** → Schema
  admits current documented shapes exactly; CI renders all sample/ci values
  files against it before merge.
- **[CA file replaces system roots]** → Documented in flag help and README; a
  bundle can contain multiple PEM blocks, covering device + intermediate.

## Migration Plan

All new behavior is opt-in or default-compatible: no flag/value changes are
required to upgrade. `log-format`/`log-level` default to current output;
CA file is optional; egress NetworkPolicy renders only when enabled; the
numeric cleanup cap defaults to unlimited. The single default-on behavior
change is the empty-desired cleanup refusal, which converts a silent
mass-delete into an error log + metric — operators intentionally emptying a
zone add the override flag (release notes will call this out). Rollback is
`helm rollback`; no state migration exists (FortiGate records are the only
state and are untouched by this change).

## Open Questions

None blocking. Two deferred follow-ups recorded for future changes: cosign
signing + SLSA provenance for published artifacts, and bridging klog into the
structured logger.
