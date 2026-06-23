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

Example manifests are available under `manifests/`. They use placeholder values and Secret references only.

## Validation

```sh
make test
make static
make helm-template
make image
make smoke
make validate
```

`make image` builds a static Linux binary and creates a local Podman image from `scratch`.

## Security Notes

- Do not commit real FortiGate URLs, tokens, private DNS zones, private IPs, kubeconfigs, or TLS keys.
- Use Kubernetes Secrets for FortiGate API credentials.
- Run with `--dry-run` first.
- Use `--domain-filter` and `--owner-id` to avoid touching unrelated records.

## License and Attribution

This project uses the Apache License 2.0. It is inspired by Kubernetes SIGs ExternalDNS concepts, but this repository keeps the implementation FortiGate-specific.
