# Validation

All validation runs locally; this repository intentionally ships no GitHub
Actions workflows. Use a repository-local `GOCACHE` so the module/build cache
stays inside the working tree.

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

# Repository safety checks
make no-workflows   # asserts no .github/workflows files exist
make secret-scan    # scans tracked files for committed API tokens

# Everything
make validate
```

## Manifest / RBAC checks

- Rendered chart and `manifests/rbac.yaml` grant only `get`/`list` on the
  watched resources (no unused `watch` verb) and `get`/`create`/`update` on
  `coordination.k8s.io/leases` when leader election is enabled.
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

## Public repository safety

- `.github/workflows` contains no workflow files.
- Secret scan finds no committed FortiGate tokens, bearer tokens, or private keys.
- No DNS provider other than FortiGate, service mesh source, or arbitrary CRD
  scanning is present in controller or RBAC code.
