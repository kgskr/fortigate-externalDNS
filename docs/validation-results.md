# Validation

Local validation runs via the Makefile; GitHub Actions additionally runs the
same checks on push/PR and publishes release artifacts when a GitHub Release is
published (see `.github/workflows/`).
Use a repository-local `GOCACHE` so the module/build cache stays inside the
working tree.

## Local commands

```sh
export GOCACHE="$PWD/.gocache"

# Unit tests and static analysis
go test ./...
go vet ./...

# Helm lint + template render (downloads helm via `go run` if not installed)
make helm-template

# Namespace-scoped + leader-election render
go run helm.sh/helm/v3/cmd/helm@v3.21.2 template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set 'namespaces[0]=apps' \
  --set leaderElection.enabled=true

# Dry-run reconcile smoke test
go test ./internal/controller -run TestDryRunSmoke -v

# Go vulnerability scan (same version CI pins)
go run golang.org/x/vuln/cmd/govulncheck@v1.5.0 ./...

# Static binary + container image
make build
make image

# Image vulnerability scan (reproduces the CI/scheduled Trivy gate; requires
# a local trivy install; .trivyignore is the accepted-findings escape hatch)
trivy image --severity HIGH,CRITICAL --ignore-unfixed \
  --ignorefile .trivyignore localhost/fortigate-external-dns:dev

# Repository safety checks
make secret-scan         # scans tracked files for committed API tokens
make secret-scan-test    # regression tests for placeholder allowlist behavior

# Everything
make validate
```

`make helm-template` also validates values against `values.schema.json` (helm
enforces the schema on lint/template) and asserts: probes render with
`metrics.enabled=false`, the egress NetworkPolicy renders its FortiGate CIDR,
the CA bundle renders a ConfigMap + `--fortigate-ca-file`, and
`caBundle`+`insecureSkipVerify` together fail the render.

## Manifest / RBAC checks

- Rendered chart and `manifests/rbac.yaml` grant only `get`/`list` on the
  watched resources (no unused `watch` verb). The raw manifests scope those
  reads to the `default` namespace with a Role/RoleBinding; set matching
  `--namespace` and RBAC values when copying them to another namespace.
  Leader-election RBAC grants `create` on `coordination.k8s.io/leases`
  namespace-wide and restricts `get`/`update` to the single lease via
  `resourceNames`.
- In namespaced mode (`namespaces` set) with the gateway source enabled, the chart
  also renders a Role/RoleBinding granting `get`/`list` on `gateways` in each
  `gatewayTargetNamespaces` entry, so HTTPRoute parent-Gateway lookup does not fail
  with `forbidden`. (HTTPRoutes are read only in the source namespaces, so the
  target-namespace Role grants `gateways` only.)
- The rendered Deployment sets `runAsNonRoot`, `allowPrivilegeEscalation: false`,
  drops all capabilities, sets `readOnlyRootFilesystem: true` and
  `seccompProfile: RuntimeDefault`, and includes resource requests/limits and
  `/healthz`/`/readyz` probes.

## Container build

- `make build` produces a static `bin/fortigate-external-dns` for local use.
- `make image` builds the multi-stage `Containerfile` with Podman for the host
  architecture (the static binary is cross-compiled inside the builder stage).
  The runtime image (`gcr.io/distroless/static-debian12:nonroot`) runs as
  non-root and includes CA certificates for HTTPS FortiGate endpoints. If the
  local Podman/Docker machine is unreachable, start it and rerun `make image`.
- CI builds and pushes a multi-arch image (`linux/amd64`, `linux/arm64`) from the
  same `Containerfile` using buildx only from the release-published workflow;
  cross-compilation in the builder stage avoids QEMU emulation.

## Release publishing

- `CI` validates pull requests and `main` pushes. It does not publish artifacts.
- `Release` runs only for GitHub `release.published` events on `v*` tag refs.
  A raw `main` push or raw tag push does not publish GHCR artifacts.
- Release publishing reuses the CI validation workflow before pushing the
  multi-arch image and Helm chart to GHCR with the built-in `GITHUB_TOKEN`,
  and stamps the release tag/commit into the binary (`--version`, `build_info`).
- The Containerfile builder image is kept compatible with the `go.mod` Go
  directive so image publishing cannot fail from a toolchain mismatch after
  dependency updates. CI now enforces this on every pull request by building
  the container image (single-arch, never pushed) before merge.

## Supply-chain checks

- Containerfile base images are pinned by multi-arch manifest-list digest;
  workflow actions are pinned to commit SHAs with version comments. Dependabot
  (weekly) tracks `gomod`, `github-actions`, and `docker` so the pins update
  via reviewed pull requests instead of drifting or going stale.
- CI runs `govulncheck` (pinned version) against the module and a Trivy scan
  against the CI-built image, failing on fixable HIGH/CRITICAL findings;
  accepted findings must be listed in the tracked `.trivyignore` with a reason.
- `scheduled-scan.yml` re-runs govulncheck and rescans the latest published
  release image weekly (`workflow_dispatch` for on-demand runs); findings fail
  the run and create-or-update a `security-scan` issue (deduplicated against an
  open one) with a link to the failing run. Its token permissions are
  `contents: read` + `issues: write` only.

## Correctness & operability hardening

Verified for the `harden-dns-correctness-and-operability` change:

- `go test ./...`, `go vet ./...`, `make helm-template`, `make smoke`, and
  `make secret-scan` all pass. (`make image` requires a reachable Podman/Docker
  machine and is run separately.)
- New `Config.Validate` rules fire at startup end-to-end (`go run` against the
  binary): a malformed `--metrics-addr`, an owner ID containing `;`/`=`, and
  `--fortigate-retries` above the cap are rejected before any work; a
  differently-cased `--provider=FortiGate` is accepted.
- A failed metrics/probe bind is now fatal (synchronous `net.Listen` before
  readiness is reported), and `--fortigate-api-token` logs a startup warning.
- `secret-scan.sh` catches a token embedded in a Secret `data`/`stringData`
  field and a real token on a line that also mentions `example`, while still
  ignoring documented placeholders.
- A live `--dry-run --once` against a FortiGate device requires a cluster and
  device; the dry-run reconcile/plan path is covered locally by `make smoke`.

Additional post-security-scan hardening covered by tests and OpenSpec baseline:

- Planner logical-record conflicts block create/update and suppress stale cleanup
  for the same conflicted zone/name/type so a contested DNS name is not partially
  mutated.
- HTTPRoute parent status must be current for the route generation; stale
  `Accepted=True` / `ResolvedRefs=True` conditions do not authorize publishing.
- Gateway listener records remain desired when HTTPRoute discovery succeeds with
  zero routes, preventing Gateway records from being deleted after the last
  HTTPRoute is removed.
- FortiGate API token help/default rendering is tested to avoid leaking
  `FORTIGATE_API_TOKEN`, while explicit token flags still override environment
  values.
- Secret-scan regression tests cover placeholder allowlisting and real-token
  matches on lines that also mention placeholder words.

## Supply-chain, security-depth, and operability hardening

Verified for the `harden-supply-chain-security-operability` change (2026-07-04):

- `go test -race ./...`, `go vet ./...`, gofmt, `make helm-template`,
  `make smoke`, and both secret scans pass. The digest-pinned Containerfile
  builds successfully with Podman (from a context copy outside `~/Documents`
  when macOS denies Podman access to the working tree).
- `--version` prints the ldflags-stamped version/commit and exits 0 without any
  other configuration; `LOG_FORMAT=json` produces JSON log lines including for
  configuration-validation errors; invalid `--log-format`/`--log-level` values
  fail startup.
- CA trust verified end-to-end against httptest TLS servers: a private-CA
  server verifies via `--fortigate-ca-file` and fails without it; a
  TLS-1.1-only listener is refused; combining the CA file with
  `--fortigate-insecure-skip-verify` (or `caBundle` + `insecureSkipVerify` in
  the chart) fails validation/render.
- Mass-cleanup guard verified through full reconciles: an empty-desired cycle
  applies no cleanup and increments `cleanup_refused_total{reason="empty-desired"}`;
  the recovery cycle cleans up normally; `--max-cleanup-per-cycle` refuses only
  cleanup while creates/updates still apply; a failed discovery aborts before
  planning.
- Heartbeat liveness verified: a wedged leader turns `/healthz` 503, a
  failing-but-attempting loop and non-leaders stay 200, `/readyz` semantics are
  unchanged, and the window auto-derives `max(5*interval, 5m)`.
- Chart renders eyeballed and script-asserted for: default values,
  `metrics.enabled=false` (probes and `--metrics-addr` bind retained, no
  Service), egress NetworkPolicy (FortiGate CIDR present), and CA bundle
  (ConfigMap + read-only mount + flag). `values.schema.json` validates the
  default, CI, and sample values files.

## Public repository safety

- GitHub Actions workflows authenticate to GHCR with the built-in `GITHUB_TOKEN`
  and contain no hardcoded credentials.
- Secret scan finds no committed FortiGate tokens, bearer tokens, or private keys.
- No DNS provider other than FortiGate, service mesh source, or arbitrary CRD
  scanning is present in controller or RBAC code.
