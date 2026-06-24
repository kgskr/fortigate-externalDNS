# FortiGate ExternalDNS

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
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Bounds each reconcile loop, including Kubernetes list and FortiGate calls. |
| `--leader-election` | `LEADER_ELECTION` | `true` | Lease-based single-writer guard for multi-replica deployments. Ignored with `--once`. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease name. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | pod namespace | Namespace for the Lease. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | Bind address for `/healthz`, `/readyz`, and `/metrics`. Empty disables the server. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (none) | Extra namespaces consulted only to resolve parent Gateway addresses. Lookup scope only; does not expand ownership or cleanup. |

Metrics are exposed in Prometheus text format under the `fortigate_external_dns_`
prefix (reconcile counters, a reconcile duration histogram, planned-operation
counters, and a last-successful-reconcile timestamp). No tokens or record
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

## Raw Manifests

Minimal reference manifests are available under `manifests/`. They use placeholder values and Secret references only, and mirror the Helm chart's security defaults (non-root, read-only root filesystem, dropped capabilities, `RuntimeDefault` seccomp, resource requests/limits) plus leader-election Lease RBAC. The Helm chart is the authoritative, fully configurable artifact.

## Validation

```sh
make test
make static
make helm-template
make image
make smoke
make validate
```

`make image` builds a static Linux binary and creates a local Podman image based on `gcr.io/distroless/static-debian12:nonroot`, which runs as a non-root user and ships with CA certificates for TLS verification.

`make validate` additionally runs `make no-workflows` (asserts no GitHub Actions workflow files are present) and `make secret-scan` (scans tracked files for committed API tokens).

## Security Notes

- Do not commit real FortiGate URLs, tokens, private DNS zones, private IPs, kubeconfigs, or TLS keys.
- Use Kubernetes Secrets for FortiGate API credentials.
- Run with `--dry-run` first.
- Use `--domain-filter` and `--owner-id` to avoid touching unrelated records.

## License and Attribution

This project uses the Apache License 2.0. It is inspired by Kubernetes SIGs ExternalDNS concepts, but this repository keeps the implementation FortiGate-specific.
