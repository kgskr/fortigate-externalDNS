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

Write mode supports an **exclusive FortiGate DNS database** only. With complete, unrestricted discovery, every record returned from the configured database is treated as controller-owned; shared zones and manually managed records in that database are not supported. The owner ID remains a diagnostic identity and is not persisted in undocumented FortiGate record fields.

Supported record types are derived from target values:

- IPv4 target -> `A`
- IPv6 target -> `AAAA`
- DNS name target -> `CNAME`

### Reconciliation Safety

- FortiGate list pagination must complete with a stable revision before planning;
  truncated or changing snapshots fail closed.
- Replacement cleanup waits until every desired target is observable, and a
  failed create cannot trigger dependent deletion of the last known-good target.
- Gateway listener records remain desired when Gateway API is installed and the
  HTTPRoute list is simply empty. The controller only skips Gateway discovery
  when the Gateway API resources themselves are unavailable.
- HTTPRoute targets are published only from parent Gateway references whose
  `Accepted=True` and `ResolvedRefs=True` conditions match the route's current
  generation.
- FortiGate API tokens can be supplied through `FORTIGATE_API_TOKEN` or
  `--fortigate-api-token`; generated help/default text never includes the token
  value.

## FortiOS Compatibility

The controller uses only the stable CMDB REST API
(`/api/v2/cmdb/system/dns-database/{zone}/dns-entry`) with `Authorization: Bearer`
token authentication. The fields it reads/writes (`hostname`, `type`, `ip`,
`ipv6`, `canonical-name`, `ttl`, `status`) and the integer record key
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
  the `dns-entry` records inside that zone; it does not create the zone. Write
  mode requires the entire database to be exclusive to this controller.
- On FortiOS 8.0 the device enforces HTTPS for token auth. The controller
  defaults to `https://` and only accepts `http`/`https` URLs. For a device
  presenting a private-CA certificate, supply the issuing chain via
  `--fortigate-ca-file` (or the chart's `fortigate.caBundle`) instead of
  disabling verification with `--fortigate-insecure-skip-verify`; the two are
  mutually exclusive and both are independent of the HTTPS requirement.
- Compatibility is verified against Fortinet's published documentation. Before a
  production rollout on a specific firmware, run a `--dry-run --once` pass
  against the target device — the controller validates the FortiGate response
  envelope and will surface a schema or API mismatch safely.

## Configuration

Configuration can be provided through flags or environment variables. FortiGate credentials should come from a Kubernetes Secret. The FortiGate base URL must not contain URL userinfo, query parameters, or a fragment; API authentication is accepted only through the token setting.

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
  --fortigate-exclusive-zone-ownership \
  --dry-run \
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
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | What to do with stale records in the exclusive database: `delete` (destructive), `deactivate` (disable but retain), or `keep` (never remove). Restricted sources or namespaces require `keep`. |
| `--allow-empty-desired-cleanup` | `ALLOW_EMPTY_DESIRED_CLEANUP` | `false` | Mass-cleanup guard override. By default, a cycle whose *successful* discovery finds zero desired endpoints refuses all cleanup — that state is the signature of a misconfiguration (wrong `--domain-filter` or `--namespace`), not a teardown. Enable only for intentional decommissioning. |
| `--max-cleanup-per-cycle` | `MAX_CLEANUP_PER_CYCLE` | `0` | Refuses a cycle's cleanup when more than this many delete/deactivate operations are planned (`0` = unlimited). Creates and updates still apply; refusals are logged at error level and counted in `cleanup_refused_total`. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Bounds each reconcile loop, including Kubernetes list and FortiGate calls. |
| `--leader-election` | `LEADER_ELECTION` | `true` | Lease-based single-writer guard for multi-replica deployments. Ignored with `--once`. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease name. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | pod namespace | Namespace for the Lease. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | Bind address for `/healthz`, `/readyz`, and `/metrics`. Empty disables the server (and with it the probes). |
| `--healthz-max-staleness` | `HEALTHZ_MAX_STALENESS` | `0` (auto) | Liveness heartbeat window: while this replica is responsible for reconciling (leader, or leader election disabled), `/healthz` fails once no reconcile attempt has *completed* within the window, so a wedged loop is restarted. Attempts that fail still count — a FortiGate outage does not restart the pod. `0` derives `max(5×interval, 5m)`. |
| `--fortigate-ca-file` | `FORTIGATE_CA_FILE` | (none) | Path to a PEM CA bundle used *instead of* system roots to verify the FortiGate TLS certificate — the right way to trust a private-CA device. Mutually exclusive with `--fortigate-insecure-skip-verify` (setting both fails validation). TLS 1.2 is the enforced minimum either way. |
| `--fortigate-exclusive-zone-ownership` | `FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP` | `false` | Required acknowledgement before writes are enabled. Confirms every record in the configured FortiGate DNS database is exclusively managed by this controller; shared/manual records are unsupported. Restricted sources or namespaces require `cleanup-policy=keep`. |
| `--log-format` | `LOG_FORMAT` | `text` | Log output format: `text` or `json` (for log aggregation pipelines). |
| `--log-level` | `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `--version` | — | — | Print the stamped version and commit, then exit. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (none) | Extra namespaces consulted only to resolve parent Gateway addresses. Lookup scope only; does not expand ownership or cleanup. In namespaced installs the Helm chart auto-renders a read-only `gateways` Role in each of these namespaces. |

Metrics are exposed in Prometheus text format under the `fortigate_external_dns_`
prefix (reconcile counters, a reconcile duration histogram, operation counters
labelled by type and result — `planned`, `applied`, `failed`, `skipped`,
`conflict` — a last-successful-reconcile timestamp, a `cleanup_refused_total`
counter for mass-cleanup guard trips, and a `build_info` gauge carrying the
version/commit). No tokens or record payloads are exposed.

### Decommissioning a cluster's records

To intentionally empty the exclusive database (for example when retiring a
cluster), complete unrestricted discovery and the empty-desired guard must be
explicitly enabled for one final run:

```sh
fortigate-external-dns --once --allow-empty-desired-cleanup \
  --source=service --source=ingress --source=gateway \
  --fortigate-exclusive-zone-ownership \
  --cleanup-policy=delete ... # remaining FortiGate flags
```

Without the override, a cycle that would delete every record refuses and
reports `cleanup_refused_total{reason="empty-desired"}`.

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

Released chart versions are published as OCI artifacts to GHCR:

```sh
helm show chart oci://ghcr.io/kgskr/charts/fortigate-external-dns --version 0.1.1
```

Create a Secret first:

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token='<fortigate-api-token>'
```

Install the published chart:

```sh
helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --version 0.1.1 \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com'
```

To install directly from a source checkout instead:

```sh
git clone https://github.com/kgskr/fortigate-externalDNS.git
cd fortigate-externalDNS

helm install fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com'
```

> **The chart defaults to `dryRun: true`**: the controller discovers records and
> logs its plan but writes **nothing** to the FortiGate. This is intentional.
> First acknowledge the exclusive zone while retaining dry-run so the preview
> uses the same ownership model as write mode:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --version 0.1.1 \
>   --reuse-values \
>   --set fortigate.exclusiveZoneOwnership=true \
>   --set dryRun=true
> ```
>
> Review that plan, then enable writes without changing the ownership model:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --version 0.1.1 \
>   --reuse-values \
>   --set dryRun=false
> ```

Before enabling writes, upgrade in dry-run mode and verify that the configured
FortiGate DNS database contains no records managed by another controller or by
an operator. Existing deployments that relied on per-record comments must move
their shared/manual records to another database. When `sources` or `namespaces`
restrict discovery, set `cleanupPolicy=keep`; destructive cleanup is allowed
only with complete, unrestricted exclusive-zone discovery. Restricted mode
accepts exact current matches and creates genuinely missing names, but target,
type, TTL, or status changes to an existing row fail closed as conflicts.

Chart values are validated against `values.schema.json` at install time; every
value is documented in [charts/fortigate-external-dns/README.md](charts/fortigate-external-dns/README.md),
including the token-rotation procedure, the private-CA `fortigate.caBundle`
option, and the opt-in egress NetworkPolicy.

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
make openspec-validate
make image
make smoke
make validate
```

`make image` builds a local Podman image for the host architecture using the multi-stage `Containerfile`, which cross-compiles the static binary inside the builder stage. The runtime image is based on `gcr.io/distroless/static-debian12:nonroot`, runs as a non-root user, and ships with CA certificates for TLS verification. The release workflow publishes a multi-arch image (`linux/amd64`, `linux/arm64`) only when a GitHub Release is published for a `v*` tag.

`make validate` additionally runs strict baseline OpenSpec validation,
`make secret-scan` (scans tracked files for committed API tokens), and
`make secret-scan-test` (regression tests for quoted keys and the placeholder
allowlist).

Continuous integration runs in GitHub Actions (see `.github/workflows/`): a CI workflow validates every pull request and default-branch push (tests, vet, gofmt, `govulncheck`, secret scan, Helm lint/template with schema validation, plus a single-arch container build scanned by Trivy — fixable HIGH/CRITICAL findings fail CI) and is reused by the release workflow to gate publishing. Publishing happens only when a GitHub Release is published for a `v*` tag; the release workflow publishes the multi-arch container image (`linux/amd64`, `linux/arm64`) to `ghcr.io/<owner>/fortigate-external-dns` and the Helm chart to GHCR as an OCI artifact, with the release tag stamped into `--version` and the `build_info` metric.

Supply-chain posture: Containerfile base images are pinned by multi-arch
manifest-list digest, workflow actions are pinned to commit SHAs, and Dependabot
(weekly) tracks the `gomod`, `github-actions`, and `docker` ecosystems so those
pins stay fresh. A scheduled weekly workflow re-runs `govulncheck` and rescans
the latest published release image with Trivy; findings fail the run **and**
create or update a `security-scan` issue so post-release CVEs surface without
anyone watching workflow runs.

## Security Notes

- Do not commit real FortiGate URLs, tokens, private DNS zones, private IPs, kubeconfigs, or TLS keys.
- Use Kubernetes Secrets for FortiGate API credentials.
- Run with `--dry-run` first.
- Keep the managed FortiGate DNS database exclusive to this controller and do
  not enable writes until `--fortigate-exclusive-zone-ownership` is intentional.
- Use `--domain-filter` to bound published hostnames; it does not make a shared
  database safe.
- Scope watched namespaces in shared clusters so lower-trust resource authors do
  not inherit the FortiGate DNS write credential.

## License and Attribution

This project uses the Apache License 2.0. It is inspired by Kubernetes SIGs ExternalDNS concepts, but this repository keeps the implementation FortiGate-specific.
