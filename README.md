# FortiGate ExternalDNS

[한국어](README.ko.md)

Kubernetes controller that publishes DNS records to a FortiGate DNS database.
Discovers hostnames and targets from **Service, Ingress, Gateway, and HTTPRoute**
resources and reconciles **A, AAAA, and CNAME** records through the FortiGate API.

Supports a single FortiGate target and CRD-backed multi-target mode with optional
shared ownership, policy, and plan approval. Platform features are disabled by
default. Other DNS providers are not supported.

## Before you install

- **Kubernetes 1.31+** for the chart's bundled CRD validation.
- A FortiGate API token and an **HTTPS** endpoint. For private certificates, set
  `fortigate.caBundle` to the issuing CA chain.
- An existing FortiGate `system dns-database` entry whose name equals its
  `domain` (for example, both `example.com`). The controller manages records,
  not the database itself; zone-apex records are not supported.
- A **dedicated database** for the default single-target mode. With default
  cleanup enabled, write mode can delete manually managed A/AAAA/CNAME records
  in that database. A domain filter does not make a shared database safe.

Live device testing covers **FortiOS 7.2.11**. See
[validation results](docs/validation-results.md) for evidence; verify other
firmware versions with dry-run before enabling writes.

## Install with Helm

Create the token Secret from a local token file:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-file=api-token=/path/to/api-token
```

Install in dry-run with the exclusive ownership model you intend to use:

```sh
helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.4.1 \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set fortigate.exclusiveZoneOwnership=true \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set dryRun=true
```

The Secret and release must be in the same namespace. To install from this
checkout, replace the OCI chart reference with `./charts/fortigate-external-dns`
and omit `--version`.

## Publish a hostname

Annotate a LoadBalancer Service; its external IP or hostname becomes the target:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  annotations:
    external-dns.kubernetes.io/hostname: web.example.com
    external-dns.kubernetes.io/ttl: "300"
spec:
  type: LoadBalancer
  selector:
    app: web
  ports:
    - port: 80
      targetPort: 8080
```

Ingress hosts and Gateway/HTTPRoute hostnames are also discovered. See
[source examples](samples/) for other resource types.

Review the planned changes:

```sh
kubectl logs deployment/fortigate-external-dns
```

Dry-run does not write to FortiGate. Once the plan and database ownership are
verified, enable writes:

```sh
helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.4.1 --reuse-values --set dryRun=false
```

If you restrict `sources` or `namespaces`, set `cleanupPolicy=keep`; restricted
mode creates missing records but rejects changes to existing records. In shared
clusters, watch only namespaces whose resource authors may publish DNS.

**Upgrades:** Helm does not upgrade CRDs. Apply the CRDs from the matching tag
before changing chart versions; see the [upgrade guide](charts/fortigate-external-dns/README.md#upgrading-the-crds).
When upgrading from v0.3.1 or earlier, review dry-run for repairs to old FQDN
hostnames and CNAME targets; `cleanupPolicy=keep` leaves old rows for manual removal.

## Reference

- [Helm values and deployment options](charts/fortigate-external-dns/README.md)
- [CLI flags and environment variables](docs/configuration.md)
- [Multi-target migration, shared ownership, recovery, and troubleshooting](docs/operations.md)
- [Raw manifests](manifests/README.md) · [One-shot plan approval](samples/one-shot-plan.sh)
- [Release verification](samples/release-verification.sh) · [Development and validation](docs/validation-results.md)
- [Security reporting](SECURITY.md) · [Apache 2.0 license](LICENSE)
