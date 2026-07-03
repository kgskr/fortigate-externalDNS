# FortiGate ExternalDNS

📖 [한국어 README](README.ko.md)

FortiGate ExternalDNS is a focused Kubernetes controller inspired by the ExternalDNS reconciliation model. It discovers DNS intent from supported Kubernetes networking resources and applies the resulting DNS records to a FortiGate device through the FortiGate API.

This project is intentionally FortiGate-only. It does not support Route53, Google Cloud DNS, Cloudflare, webhook providers, service mesh APIs, or arbitrary third-party CRDs.

## Supported Sources

- Kubernetes `Service`
- Kubernetes `Ingress`
- Kubernetes SIG Gateway API `Gateway`
- Kubernetes SIG Gateway API `HTTPRoute`

Gateway API is supported as a standard Kubernetes networking API even though it is installed as CRDs. Other CRDs are not scanned for hostname-like fields.

## DNS Scope

The controller creates, updates, and removes only records it owns. Ownership is tracked with the configured owner ID in FortiGate record metadata. Use domain filters and a dedicated owner ID per cluster.

Supported record types are derived from target values:

- IPv4 target -> `A`
- IPv6 target -> `AAAA`
- DNS name target -> `CNAME`

## FortiOS Compatibility

The controller uses only the stable CMDB REST API
(`/api/v2/cmdb/system/dns-database/{zone}/dns-entry`) with `Authorization: Bearer`
token authentication. The fields it reads/writes (`hostname`, `type`, `ip`,
`ipv6`, `canonical-name`, `ttl`, `status`, `comment`) and the integer record key
(`q_origin_key`/`id`) are consistent across the releases below.

| FortiOS | Status | Notes |
| --- | --- | --- |
| 7.0 / 7.2 / 7.4 / 7.6 | ✅ Supported | CMDB `system/dns-database` API and Bearer token auth are stable across these releases. |
| 8.0 | ✅ Supported | API tokens require **HTTPS** — plain `http://` is rejected by the device. Use an `https://` URL (the default). |
| 6.4 and earlier | ⚠️ Untested | The CMDB API and Bearer header exist from 6.0+, but these releases are not verified here. |
| 5.6 and earlier | ❌ Not supported | Predates the Bearer-token API model this controller uses. |

Notes:

- The target zone must already exist as a `config system dns-database` entry on
  the FortiGate (typically a primary/`master` zone). The controller manages only
  the `dns-entry` records it owns inside that zone; it does not create the zone.
- On FortiOS 8.0 the device enforces HTTPS for token auth. The controller
  defaults to `https://` and only accepts `http`/`https` URLs;
  `--fortigate-insecure-skip-verify` controls certificate verification and is
  independent of this.
- Compatibility is verified against Fortinet's published documentation. Before a
  production rollout on a specific firmware, run a `--dry-run --once` pass
  against the target device — the controller validates the FortiGate response
  envelope and will surface a schema or API mismatch safely.

## Configuration

Configuration can be provided through flags or environment variables. FortiGate credentials should come from a Kubernetes Secret.

Common flags:

```sh
fortigate-external-dns \
  --provider=fortigate \
  --source=service \
  --source=ingress \
  --source=gateway \
  --domain-filter=example.com \
  --owner-id=my-cluster \
  --fortigate-url=https://fortigate.example.com \
  --fortigate-zone=example.com \
  --fortigate-vdom=root
```

Required secret value:

```sh
FORTIGATE_API_TOKEN=<api-token-from-kubernetes-secret>
```

The controller rejects non-FortiGate providers.

Environment variables are parsed strictly: a non-empty value that cannot be
parsed (for example `DRY_RUN=ture` or `INTERVAL=30` without a unit) fails
startup instead of silently falling back to a default. This prevents a
mistyped `DRY_RUN` from silently enabling writes.

### Operability flags

| Flag | Env | Default | Purpose |
| --- | --- | --- | --- |
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | What to do with owned records that no longer have a matching source: `delete` (destructive — removes the record), `deactivate` (disables the record but keeps it), or `keep` (never remove). Prefer `deactivate` or `keep` for an initial rollout. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Bounds each reconcile loop, including Kubernetes list and FortiGate calls. |
| `--leader-election` | `LEADER_ELECTION` | `true` | Lease-based single-writer guard for multi-replica deployments. Ignored with `--once`. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease name. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | pod namespace | Namespace for the Lease. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | Bind address for `/healthz`, `/readyz`, and `/metrics`. Empty disables the server. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (none) | Extra namespaces consulted only to resolve parent Gateway addresses. Lookup scope only; does not expand ownership or cleanup. In namespaced installs the Helm chart auto-renders a read-only `gateways` Role in each of these namespaces. |

Metrics are exposed in Prometheus text format under the `fortigate_external_dns_`
prefix (reconcile counters, a reconcile duration histogram, operation counters
labelled by type and result — `planned`, `applied`, `failed`, `skipped`,
`conflict` — and a last-successful-reconcile timestamp). No tokens or record
payloads are exposed.

## Local Dry Run

Use dry-run mode before allowing writes:

```sh
FORTIGATE_API_TOKEN=placeholder \
go run ./cmd/fortigate-external-dns \
  --once \
  --dry-run \
  --kubeconfig "$HOME/.kube/config" \
  --source=service \
  --source=ingress \
  --source=gateway \
  --domain-filter=example.com \
  --owner-id=my-cluster \
  --fortigate-url=https://fortigate.example.com \
  --fortigate-zone=example.com
```

## Helm Install

Create a Secret first:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token='<fortigate-api-token>'
```

Install with the chart:

```sh
helm install fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com
```

For shared or multi-tenant clusters, set `namespaces` to the namespaces whose
resource authors are allowed to publish DNS records. Leaving it empty watches all
namespaces and should be reserved for clusters where Service, Ingress, Gateway,
and HTTPRoute authors are trusted to publish records in the configured zone.

## Raw Manifests

Minimal reference manifests are available under `manifests/`. They are scoped to
the `default` namespace by default, use placeholder values and Secret references
only, and mirror the Helm chart's security defaults (non-root, read-only root
filesystem, dropped capabilities, `RuntimeDefault` seccomp, resource
requests/limits) plus leader-election Lease RBAC. The Helm chart is the
authoritative, fully configurable artifact.

## Samples

- `samples/values-existing-secret.yaml` — Helm values for installing against a pre-created FortiGate API-token Secret (`helm install ... -f samples/values-existing-secret.yaml`).
- `samples/service.yaml` — an annotated `Service` showing the hostname/TTL annotations the controller reads.

## Validation

```sh
make test
make static
make helm-template
make image
make smoke
make validate
```

`make image` builds a local Podman image for the host architecture using the multi-stage `Containerfile`, which cross-compiles the static binary inside the builder stage. The runtime image is based on `gcr.io/distroless/static-debian12:nonroot`, runs as a non-root user, and ships with CA certificates for TLS verification. CI publishes a multi-arch image (`linux/amd64`, `linux/arm64`).

`make validate` additionally runs `make secret-scan` (scans tracked files for
committed API tokens) and `make secret-scan-test` (regression tests for the
placeholder allowlist).

Continuous integration runs in GitHub Actions (see `.github/workflows/`): a CI workflow validates every pull request (tests, vet, gofmt, secret scan, Helm lint/template) and is reused by the release workflow to gate publishing, so pushes to the default branch and version tags are validated before anything is published. The release workflow then publishes the multi-arch container image (`linux/amd64`, `linux/arm64`) to `ghcr.io/<owner>/fortigate-external-dns` (on the default branch and version tags) and the Helm chart to GHCR as an OCI artifact (on version tags).

## Security Notes

- Do not commit real FortiGate URLs, tokens, private DNS zones, private IPs, kubeconfigs, or TLS keys.
- Use Kubernetes Secrets for FortiGate API credentials.
- Run with `--dry-run` first.
- Use `--domain-filter` and `--owner-id` to avoid touching unrelated records.
- Scope watched namespaces in shared clusters so lower-trust resource authors do
  not inherit the FortiGate DNS write credential.

## License and Attribution

This project uses the Apache License 2.0. It is inspired by Kubernetes SIGs ExternalDNS concepts, but this repository keeps the implementation FortiGate-specific.
