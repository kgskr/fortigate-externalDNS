# Validation

Local validation runs via the Makefile; GitHub Actions additionally runs the
same checks on push/PR and publishes release artifacts (see `.github/workflows/`).
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

# Static binary + container image
make build
make image

# Repository safety check
make secret-scan    # scans tracked files for committed API tokens

# Everything
make validate
```

## Manifest / RBAC checks

- Rendered chart and `manifests/rbac.yaml` grant only `get`/`list` on the
  watched resources (no unused `watch` verb). Leader-election RBAC grants
  `create` on `coordination.k8s.io/leases` namespace-wide and restricts
  `get`/`update` to the single lease via `resourceNames`.
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

- `make build` produces a static `bin/fortigate-external-dns`.
- `make image` builds `gcr.io/distroless/static-debian12:nonroot`; the runtime
  image runs as non-root and includes CA certificates for HTTPS FortiGate
  endpoints. If the local Podman/Docker machine is unreachable, start it and
  rerun `make image`.

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

## Public repository safety

- GitHub Actions workflows authenticate to GHCR with the built-in `GITHUB_TOKEN`
  and contain no hardcoded credentials.
- Secret scan finds no committed FortiGate tokens, bearer tokens, or private keys.
- No DNS provider other than FortiGate, service mesh source, or arbitrary CRD
  scanning is present in controller or RBAC code.
