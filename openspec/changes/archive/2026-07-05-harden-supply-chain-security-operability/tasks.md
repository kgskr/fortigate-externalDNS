# Tasks: harden-supply-chain-security-operability

## 1. Configuration surface (internal/config)

- [x] 1.1 Add `--fortigate-ca-file` / `FORTIGATE_CA_FILE` to `Config`, flag registration, env loading, and `Redacted()`; validation rejects combining it with `--fortigate-insecure-skip-verify` and rejects a missing/unreadable/non-PEM file (read+parse at validation time)
- [x] 1.2 Add `--log-format` (`text|json`) and `--log-level` (`debug|info|warn|error`) flags with env equivalents, strict-parse validation, defaults `text`/`info`
- [x] 1.3 Add `--healthz-max-staleness` duration flag (default `0` = auto `max(5×interval, 5m)`), validated non-negative; expose resolved window via a `Config` method
- [x] 1.4 Add `--allow-empty-desired-cleanup` (bool, default false) and `--max-cleanup-per-cycle` (int, default 0 = unlimited, validated non-negative) with env equivalents
- [x] 1.5 Extend `internal/config` tests: CA/skip-verify conflict, malformed CA file, invalid log format/level, negative cap rejection, staleness auto-derivation, and help output still leaks no secret defaults

## 2. FortiGate client TLS (internal/fortigate)

- [x] 2.1 Build `tls.Config` with `MinVersion: tls.VersionTLS12` always; when a CA file is configured, load the PEM bundle into `RootCAs` (replacing system roots); keep `InsecureSkipVerify` path unchanged otherwise
- [x] 2.2 Client tests: httptest TLS server with a private CA verifies successfully via CA file, fails without it, and a TLS-1.1-only listener is refused

## 3. Mass-cleanup guard (internal/plan, internal/controller, internal/metrics)

- [x] 3.1 Implement the guard where cleanup operations are assembled: refuse all cleanup ops for the cycle when (a) desired set is empty, owned records exist, and override is off, or (b) planned cleanup count exceeds a non-zero cap; creates/updates in the same cycle are unaffected
- [x] 3.2 Add `fortigate_external_dns_cleanup_refused_total` counter; increment on refusal; log refusal at error level with planned count and reason (empty-desired vs cap)
- [x] 3.3 Tests: empty-desired refusal (deletes and deactivates), override proceeds, cap exceeded refuses only cleanup while creates/updates apply, recovery cycle cleans up normally, discovery error still plans no cleanup

## 4. Heartbeat liveness (internal/controller, internal/serve, cmd)

- [x] 4.1 Record a monotonic reconcile-attempt-completion timestamp in the runner (success or failure both count); expose a getter safe for concurrent probe reads
- [x] 4.2 Extend `internal/serve` so `/healthz` consults an optional staleness check: non-success only when the replica is currently responsible for reconciling (leading, or leader election disabled) and the last attempt is older than the window
- [x] 4.3 Wire leadership state and the staleness window in `cmd/fortigate-external-dns/main.go` (arm on `OnStartedLeading`, disarm on stop; armed always in `--once`/no-LE mode only while running)
- [x] 4.4 Tests: wedged-leader returns 503, attempting-but-failing loop stays 200, non-leader stays 200, window auto-derivation honored, `/readyz` semantics unchanged

## 5. Version identity and logging (cmd, internal/metrics, Makefile, Containerfile)

- [x] 5.1 Add `version`/`commit` vars in `main` stamped via `-ldflags -X`; implement `--version` printing `fortigate-external-dns <version> (<commit>)` and exiting 0 before config validation
- [x] 5.2 Add `build_info` gauge (value 1, labels `version`, `commit`) to the metrics registry and its test
- [x] 5.3 Thread `VERSION`/`COMMIT` build args through `Makefile` `build` target and `Containerfile` (defaults `dev`/`unknown`)
- [x] 5.4 Construct the slog handler from `--log-format`/`--log-level` in `main.go`; replace the bare `fmt.Fprintln` shutdown message with the logger; add a main test covering handler selection

## 6. Helm chart

- [x] 6.1 Move container port, liveness, and readiness probes out of the `metrics.enabled` conditional in `templates/deployment.yaml`; `metrics.enabled` now gates only the metrics Service and its ingress NetworkPolicy; align default probe timings with the default staleness window
- [x] 6.2 Add `fortigate.caBundle` value: render a ConfigMap and read-only mount at `/etc/fortigate-external-dns/ca/ca.crt`, pass `--fortigate-ca-file` when set
- [x] 6.3 Add opt-in egress NetworkPolicy template (deny-all egress + DNS, kube API, FortiGate peer/port values), disabled by default
- [x] 6.4 Expose new controller options in values (`allowEmptyDesiredCleanup`, `maxCleanupPerCycle`, `logFormat`, `logLevel`, `healthzMaxStaleness`) mapped to args only when non-default
- [x] 6.5 Add `values.schema.json` covering every value (types, enums for cleanupPolicy/logFormat/logLevel); ensure default, `ci/existing-secret-values.yaml`, and `samples/values-existing-secret.yaml` all validate
- [x] 6.6 Add chart `README.md` documenting every value (default + purpose), the token-rotation procedure (`kubectl rollout restart`, reloader annotation note), and the egress/CA options
- [x] 6.7 Add `NOTES.txt` reporting dry-run state (with the enable-writes command), metrics Service state, and the Secret name in use
- [x] 6.8 Update `scripts/helm-template-check.sh` to assert probes render with `metrics.enabled=false`, schema validation runs, and rendered egress policy contains the FortiGate peer

## 7. Raw manifests alignment

- [x] 7.1 Mirror the probe wiring (independent of metrics) and any changed defaults in `manifests/deployment.yaml`; confirm no new flags need to appear (all default-compatible) and note the dry-run/live difference explicitly in a manifest comment

## 8. Supply-chain pinning and Dependabot

- [x] 8.1 Pin `Containerfile` base images by multi-arch manifest-list digest (keep tag in reference); verify local `make image` still builds
- [x] 8.2 Pin every action in `ci.yml` and `release.yml` to full commit SHAs with version comments
- [x] 8.3 Add `docker` ecosystem (weekly) to `.github/dependabot.yml`

## 9. CI scanning

- [x] 9.1 Add a `govulncheck` step to the reusable CI validation workflow
- [x] 9.2 Add a CI job that builds the container image (single-arch, `push: false`, buildx layer cache) so PRs gate on the container build
- [x] 9.3 Add a Trivy scan of the CI-built image failing on fixable HIGH/CRITICAL, with a tracked `.trivyignore` (empty initially) as the escape hatch
- [x] 9.4 Confirm `release.yml`'s `workflow_call` gating picks up the new validation steps unchanged

## 10. Scheduled rescan workflow

- [x] 10.1 Add `.github/workflows/scheduled-scan.yml`: weekly cron + `workflow_dispatch`; jobs run govulncheck on the default branch and Trivy against the latest published release image; permissions `contents: read`, `issues: write`
- [x] 10.2 On failure, create-or-update a single open issue labeled `security-scan` (dedup via `gh issue list`), appending the failing run link

## 11. Documentation

- [x] 11.1 README.md + README.ko.md: state the chart's `dryRun: true` default in the Helm install steps with the `--set dryRun=false` command; document `--fortigate-ca-file`, the CA/skip-verify conflict rule, `--allow-empty-desired-cleanup` / `--max-cleanup-per-cycle` (next to `--cleanup-policy`, with a `--once` decommissioning recipe), `--log-format`/`--log-level`, `--version`, and the heartbeat liveness behavior
- [x] 11.2 Update `docs/validation-results.md` with the new local validation steps (schema render, govulncheck, trivy repro commands) and re-run results

## 12. Final validation

- [x] 12.1 `make validate` passes end to end (fmt, tests incl. `-race` locally, vet, helm template check, image build, smoke, secret scans)
- [x] 12.2 Render chart with default, metrics-disabled, egress-enabled, and CA-bundle values; eyeball rendered Deployment/NetworkPolicy/ConfigMap for correctness
- [x] 12.3 Verify `--version`, JSON log output, and `build_info` metric from a locally built binary; verify guard refusal path once via dry-run-style unit/smoke coverage
