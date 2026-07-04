# Proposal: harden-supply-chain-security-operability

## Why

A full project review (2026-07-04) found that while the controller's code-level
security and release design are sound, the project has no automated way to learn
that a patch is needed (no base-image tracking, no vulnerability scanning, no
scheduled rebuild — a broken toolchain bump reached `main` and silently blocked a
security release), operators of private-CA FortiGate devices are pushed toward
`--fortigate-insecure-skip-verify` because there is no custom CA option, and a
successfully-empty discovery can plan mass deletion of every owned record. This
change closes those gaps in one hardening pass across supply chain, security
depth, and day-2 operability.

## What Changes

### Supply chain — "know you need a patch"

- Add `docker` ecosystem to Dependabot so Containerfile base images are tracked.
- Pin Containerfile base images by digest; Dependabot keeps digests fresh.
- Pin all GitHub Actions in workflows to full commit SHAs (with version comments).
- Add `govulncheck` (Go vulnerability DB) to CI validation.
- Build the container image in CI and scan it with Trivy, failing on fixable
  HIGH/CRITICAL findings.
- Add a weekly scheduled workflow that re-runs govulncheck and rescans the latest
  published release image, failing loudly (issue creation) on new findings.

### Security depth

- New `--fortigate-ca-file` / `FORTIGATE_CA_FILE` option loading a PEM CA bundle
  for FortiGate TLS verification; explicit TLS `MinVersion` 1.2; configuration
  validation rejects setting both a CA file and insecure-skip-verify (fail
  closed on contradictory trust config). Chart wiring via a `caBundle` value
  mounted read-only.
- Opt-in egress NetworkPolicy in the chart (deny-all egress with allowlist for
  kube API, DNS, and the FortiGate endpoint), disabled by default.
- Mass-deletion safety cap: a reconcile that plans more than a configured number
  of owned-record deletions refuses to delete that cycle, surfaces an error and a
  metric, and requires an explicit override to proceed.

### Operability

- Probe endpoints (`/healthz`, `/readyz`) remain served even when chart
  `metrics.enabled=false` (today the probes are dropped with the metrics port).
- Liveness reflects reconcile health: `/healthz` fails when no reconcile attempt
  has completed within a configurable multiple of the interval.
- Document the chart's `dryRun: true` default in both READMEs' install steps.
- Add `values.schema.json`, a chart README documenting every value, and
  `NOTES.txt` post-install hints.
- Document the token-rotation restart path (`kubectl rollout restart`) for
  `existingSecret` users; support pod-annotation passthrough for reloader-style
  tooling.
- Add `--log-format=text|json`, `--log-level`, and `--version` (ldflags-stamped
  version/commit) plus a `build_info` metric.

No breaking changes: all new flags/values default to current behavior
(`log-format=text`, no CA file, egress policy off, deletion cap generous).

## Capabilities

### New Capabilities

- `supply-chain-security`: how build inputs are pinned (base-image digests,
  action SHAs), how updates are tracked (Dependabot ecosystems), and how
  vulnerabilities are detected before and after release (CI govulncheck + Trivy,
  weekly scheduled rescan with loud failure).

### Modified Capabilities

- `controller-operability`: probe endpoints decoupled from metrics exposure;
  liveness tied to a reconcile heartbeat; FortiGate TLS trust configurable via CA
  bundle with enforced minimum TLS version; structured log format/level flags;
  version reporting (`--version`, `build_info` metric).
- `reconciliation-data-safety`: bounded deletion per reconcile cycle with
  explicit override (mass-deletion safety cap).
- `deployment-artifact-consistency`: chart gains egress NetworkPolicy option,
  values schema, chart README, NOTES.txt, CA-bundle and annotation passthrough
  wiring; install docs state the dry-run default and token-rotation procedure;
  raw manifests stay aligned with new chart defaults.

## Impact

- **Go code**: `internal/config` (new flags/env), `internal/fortigate` (TLS
  config), `internal/plan` or `internal/controller` (deletion cap), `internal/serve`
  + `internal/controller` (heartbeat liveness), `internal/metrics` (`build_info`,
  deletion-cap metric), `cmd/fortigate-external-dns` (logger setup, `--version`).
- **Chart**: `values.yaml`, `values.schema.json` (new), `templates/deployment.yaml`
  (probes, CA mount, annotations), `templates/networkpolicy-egress.yaml` (new),
  `README.md` (new), `NOTES.txt` (new).
- **Manifests**: `manifests/deployment.yaml` probe/arg alignment as needed.
- **CI/CD**: `.github/dependabot.yml`, `.github/workflows/ci.yml`,
  `.github/workflows/release.yml`, new `.github/workflows/scheduled-scan.yml`;
  `Containerfile` digest pins and ldflags version stamp; `Makefile` build flags.
- **Docs**: `README.md`, `README.ko.md`, `docs/validation-results.md`.
- **Dependencies**: no new Go module dependencies (govulncheck/Trivy run in CI
  only; JSON logging uses stdlib `log/slog`).
