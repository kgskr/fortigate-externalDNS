# fortigate-external-dns Helm chart

Deploys the FortiGate-only, ExternalDNS-inspired controller that publishes DNS
intent from Kubernetes `Service`, `Ingress`, and Gateway API resources into a
FortiGate `system dns-database` zone.

## Install

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token=<FORTIGATE_API_TOKEN>

helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com
```

> **The chart defaults to `dryRun: true`.** The controller logs the plan but
> writes nothing to the FortiGate. Verify the logged plan, then enable writes:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --reuse-values --set dryRun=false
> ```

Values are validated against [values.schema.json](values.schema.json) at
install time, so a misspelled key or out-of-range value fails fast.

## Token rotation

The chart never creates the token Secret; it references your
`fortigate.existingSecret`. Kubernetes does not restart pods when a Secret
changes, so after rotating the token:

```sh
kubectl -n <namespace> rollout restart deployment/fortigate-external-dns
```

Alternatively, annotate the pod for reloader-style controllers (for example
[stakater/Reloader](https://github.com/stakater/Reloader)) via `podAnnotations`.

## Trusting a private-CA FortiGate

Most FortiGate management interfaces present a private-CA or self-signed
certificate. Instead of `fortigate.insecureSkipVerify` (which disables
verification entirely and is rejected in combination with a CA bundle), supply
the issuing CA chain:

```yaml
fortigate:
  caBundle: |
    -----BEGIN CERTIFICATE-----
    ...
    -----END CERTIFICATE-----
```

The bundle is rendered into a ConfigMap, mounted read-only, and passed via
`--fortigate-ca-file`. It replaces the system roots for FortiGate connections;
TLS 1.2 is the enforced minimum.

## Egress containment

The controller holds a firewall-admin token. `egressNetworkPolicy` (opt-in)
denies all egress except DNS, the Kubernetes API, and the FortiGate endpoint:

```yaml
egressNetworkPolicy:
  enabled: true
  fortigate:
    cidr: 203.0.113.10/32   # required when enabled
    port: 443
```

## Health probing and metrics

The probe/metrics HTTP server always runs on `metrics.port`; liveness and
readiness probes are always rendered. `metrics.enabled` gates only scrape
exposure (the metrics Service and its ingress NetworkPolicy). Liveness fails
when the reconciling replica completes no reconcile attempt within the
heartbeat window (`healthzMaxStaleness`, default `max(5*interval, 5m)`) — a
wedged loop restarts, while a reachable-but-erroring FortiGate does not.

## Values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Controller replicas. >1 is safe: Lease-based leader election ensures a single writer. |
| `image.repository` | `ghcr.io/kgskr/fortigate-external-dns` | Controller image. |
| `image.tag` | `""` | Image tag; empty uses the chart `appVersion` (kept in lockstep by the release workflow). |
| `image.digest` | `""` | Immutable digest (`sha256:...`); takes precedence over `tag`. Prefer in production. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Pull secrets for private registries. |
| `nameOverride` / `fullnameOverride` | `""` | Naming overrides. |
| `serviceAccount.create` | `true` | Create the ServiceAccount. |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations. |
| `serviceAccount.name` | `""` | Existing ServiceAccount name when `create=false`. |
| `rbac.create` | `true` | Create all required RBAC (source reads and, with leader election, the Lease Role). When `false` you must provide every grant yourself. |
| `sources` | `[service, ingress, gateway]` | Enabled discovery sources. |
| `namespaces` | `[]` | Namespaces to watch. Empty means all namespaces (cluster-scoped RBAC). |
| `gatewayTargetNamespaces` | `[]` | Extra namespaces consulted only to resolve parent Gateway addresses; read-only, no cleanup ownership. |
| `domainFilters` | `[]` | Domain suffixes to include. Scope this tightly per cluster. |
| `ownerID` | `fortigate-external-dns` | Ownership identity written into managed-record comments. Use a distinct value per cluster. |
| `defaultTTL` | `300` | Default record TTL (seconds). |
| `dryRun` | `true` | **Default on**: log the plan, write nothing. Set `false` to enable writes. |
| `once` | `false` | Run one reconcile loop and exit. |
| `interval` | `1m` | Reconciliation interval. |
| `reconcileTimeout` | `2m` | Per-loop timeout bounding Kubernetes and FortiGate calls. |
| `cleanupPolicy` | `delete` | Stale managed-record handling: `delete` (destructive), `deactivate`, or `keep`. |
| `allowEmptyDesiredCleanup` | `false` | Permit cleanup when a successful discovery finds zero desired endpoints. Leave off except for intentional decommissioning — it is the mass-delete misconfiguration guard. |
| `maxCleanupPerCycle` | `0` | Refuse a cycle's cleanup when more than this many delete/deactivate operations are planned (`0` = unlimited). |
| `logFormat` | `text` | Log output format: `text` or `json`. |
| `logLevel` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `healthzMaxStaleness` | `""` | Liveness heartbeat window (duration). Empty derives `max(5*interval, 5m)`. |
| `leaderElection.enabled` | `true` | Lease-based single-writer election. |
| `leaderElection.id` | `""` | Lease name; defaults to the chart fullname. |
| `leaderElection.namespace` | `""` | Lease namespace; defaults to the release namespace. |
| `metrics.enabled` | `true` | Gates scrape exposure only (Service/NetworkPolicy). Probes always render; the server always binds `metrics.port`. |
| `metrics.port` | `8080` | Pod port serving `/healthz`, `/readyz`, `/metrics`. |
| `metrics.service.enabled` | `false` | Render a ClusterIP Service for scraping. |
| `metrics.service.annotations` | `{}` | Metrics Service annotations. |
| `metrics.networkPolicy.enabled` | `false` | Ingress NetworkPolicy for the metrics port (deny-by-default; see values comment about kubelet probes). |
| `metrics.networkPolicy.allowedNamespaces` | `[]` | Namespace label selectors allowed to scrape. |
| `fortigate.url` | `""` (required) | FortiGate API base URL (`https://...`). |
| `fortigate.zone` | `""` (required) | Existing `system dns-database` zone to manage records in. |
| `fortigate.vdom` | `root` | FortiGate VDOM. |
| `fortigate.existingSecret` | `""` (required) | Secret containing the API token. The chart never creates it. |
| `fortigate.apiTokenSecretKey` | `api-token` | Key inside the Secret. |
| `fortigate.insecureSkipVerify` | `false` | Disable TLS verification. Prefer `caBundle`; mutually exclusive with it. |
| `fortigate.caBundle` | `""` | Inline PEM CA chain used instead of system roots to verify the device. |
| `fortigate.timeout` | `15s` | FortiGate API request timeout. |
| `fortigate.retries` | `2` | Retry count for retryable FortiGate failures (0–10). |
| `egressNetworkPolicy.enabled` | `false` | Opt-in deny-all egress with allowlist (DNS, kube API, FortiGate). |
| `egressNetworkPolicy.fortigate.cidr` | `""` | FortiGate management CIDR (required when enabled). |
| `egressNetworkPolicy.fortigate.port` | `443` | FortiGate API port. |
| `egressNetworkPolicy.kubeAPI.cidr` | `""` | Optional CIDR restriction for the API server. |
| `egressNetworkPolicy.kubeAPI.ports` | `[443, 6443]` | Ports allowed toward the API server. |
| `egressNetworkPolicy.dns.enabled` | `true` | Allow UDP/TCP 53 egress. |
| `podAnnotations` / `podLabels` | `{}` | Extra pod metadata (e.g. reloader annotations). |
| `resources` | requests `25m/64Mi`, limits `200m/128Mi` | Container resources. |
| `nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | Scheduling controls. |
| `securityContext` | restricted-PSS-compliant | Container security context (non-root 65532, no privilege escalation, read-only rootfs, drop ALL). |
| `podSecurityContext` | `fsGroup: 65532`, `RuntimeDefault` seccomp | Pod security context. |

## Decommissioning

To intentionally remove all managed records (for example when retiring a
cluster), run one final cycle with the empty-desired guard overridden:

```sh
helm upgrade fortigate-external-dns ... \
  --reuse-values --set sources={service} --set namespaces={none-existent} \
  --set allowEmptyDesiredCleanup=true --set once=true
```

Then uninstall the release. Without `allowEmptyDesiredCleanup=true` the
controller refuses a cycle that would delete every owned record.
