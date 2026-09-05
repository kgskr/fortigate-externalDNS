# fortigate-external-dns Helm chart

Deploys the FortiGate-only, ExternalDNS-inspired controller that publishes DNS
intent from Kubernetes `Service`, `Ingress`, and Gateway API resources into a
FortiGate `system dns-database` zone.

## Install

```sh
kubectl create secret generic fortigate-external-dns \
  --from-literal=api-token='<FORTIGATE_API_TOKEN>'

helm install fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com'
```

> **The chart defaults to `dryRun: true`.** The controller logs the plan but
> writes nothing to the FortiGate. First preview the actual exclusive ownership
> model while retaining dry-run:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --reuse-values \
>   --set fortigate.exclusiveZoneOwnership=true \
>   --set dryRun=true
> ```
>
> Verify that logged plan, then enable writes without changing ownership mode:
>
> ```sh
> helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns \
>   --reuse-values \
>   --set dryRun=false
> ```

## Exclusive-zone ownership migration

With complete, unrestricted discovery, write mode treats every record in the
configured FortiGate DNS database as owned by this controller. Shared databases
and manually managed records are not supported. Before setting
`fortigate.exclusiveZoneOwnership=true`, upgrade in
dry-run mode, inspect the plan, and move records owned by another system or
operator to a different database. If `sources` or `namespaces` restrict
discovery, use `cleanupPolicy=keep`; destructive cleanup requires complete,
unrestricted discovery of the exclusive database. Restricted mode accepts
exact current matches and creates genuinely missing names, but existing target,
type, TTL, or status changes fail closed as conflicts.

Values are validated against [values.schema.json](values.schema.json) at
install time, so a misspelled key or out-of-range value fails fast.

## Platform runtime

The chart packages the five `fortigate-external-dns.kgskr.io/v1alpha1` CRDs in
`crds/`; Helm installs those APIs before templates. All platform behavior is
disabled by default. `platform.targetMode.enabled=true` switches the Deployment
from direct connection arguments to namespaced `FortiGateDNSTarget` resources.
Targets, policies, claims, exact-hash plans, status, event processing, and source
expansion are active only when their corresponding values are enabled.

The target-mode example is safe-by-default, uses references only, and keeps
every target in dry-run:

```sh
helm template fortigate-external-dns . \
  --include-crds \
  --values ../../samples/platform-values.yaml
```

Shared targets require `platform.sharedOwnership.enabled=true`; targets with
`approvalMode=required` require `platform.planApproval.enabled=true`. Every
referenced API-token Secret, CA Secret, and CA ConfigMap must be listed in the
corresponding `platform.targetMode.*Names` allowlist so Helm can generate
resourceName-bound `get` grants. Exact or parent/child DNS scope overlap fails
rendering unless both targets are non-destructive (`cleanupPolicy=keep`) and
explicitly acknowledge overlap.

Headless Service support adds `discovery.k8s.io/endpointslices` read/watch RBAC
only when `platform.sourceExpansion.headless.enabled=true`; ExternalName and
headless remain disabled by default. Target Secret and CA values are resolved
in memory through the Kubernetes API, never mounted or rendered, and reloaded
on resync. Rotation rebuilds only the affected target client.

Discovery rejects a source object before endpoint allocation when its
hostname/target product exceeds 1,024 endpoints, or when a reconcile would
exceed 10,000 endpoints in total. The affected source becomes incomplete, which
blocks destructive cleanup for that reconcile.

### Exclusive-to-shared runbook

Keep `dryRun=true` and `cleanupPolicy=keep`, back up the FortiGate database and
platform metadata, then review every provider row against the same stable
provider revision. The current runtime rejects adoption and target/type
replacement because those claim identity transitions are not represented by the
approval contract. Keep writes stopped and migrate such rows through an audited
operator process; preserve claims/finalizers, never invent a source UID, and
never set claim status manually. Wait for every mutable record claim to be
`Confirmed` before enabling writes. The old
exclusive controller must be stopped before a shared writer starts. Rollback
starts by disabling shared writes while preserving claims and finalizers.
Changing an existing shared record's target or type requires the same
stopped-write operator process; the old claim does not authorize the new record
identity.

### Legacy-to-multi-target runbook

Model each existing Deployment as one dry-run target with identical source,
namespace, domain, VDOM, zone, cleanup, and controller identity boundaries.
Writable scopes may not overlap. An intentional overlap is allowed only when
both targets use `cleanupPolicy=keep` and `allowNonDestructiveOverlap=true`.
Enable and verify targets one at a time; authentication, TLS, policy, and API
failures remain isolated to that target. See the [target](../../samples/targets.yaml),
[policy](../../samples/policy.yaml), and
[platform values](../../samples/platform-values.yaml) examples.

## Supplying RBAC yourself

With `rbac.create=false` the chart emits no Role, ClusterRole, RoleBinding, or
ClusterRoleBinding. Grant the selected ServiceAccount only the rows required by
your enabled values. When `serviceAccount.create=false`,
`serviceAccount.name` is mandatory; the chart never falls back to the
namespace's default ServiceAccount:

| Scope | API group/resources | Verbs | When required |
| --- | --- | --- | --- |
| Source namespace(s), or cluster-wide when `namespaces=[]` | core `services`; `networking.k8s.io/ingresses`; `gateway.networking.k8s.io/gateways,httproutes` | `get,list`; add `watch` only with `platform.events.enabled` | Matching entries in `sources` |
| Source namespace(s), or cluster-wide | `discovery.k8s.io/endpointslices` | `get,list,watch` | Headless source expansion |
| Gateway target namespaces | `gateway.networking.k8s.io/gateways` | `get,list`; add `watch` with events | `gatewayTargetNamespaces` |
| Leader-election namespace | `coordination.k8s.io/leases` | `create`; `get,update` restricted to the configured Lease name | `leaderElection.enabled` |
| Release namespace | `fortigatednstargets` | `get,list,watch`; target `/status`: `get,update,patch`; `/finalizers`: `update` | Target mode |
| Release namespace | `fortigatednsrecordownerships`, `fortigatednschangeplans`, `fortigatednsstatuses` | Main resource: `get,list,watch,create,update,patch,delete`; `/status`: `get,update,patch`; `/finalizers`: `update` | Shared ownership, approval, or status respectively |
| Release namespace | `fortigatednspolicies` | `get,list,watch` | Policy enforcement |
| Release namespace | named core `secrets` / `configmaps` | `get`, restricted with `resourceNames` | Every referenced token/CA object |
| Release namespace | core `events` | `create,patch` | Enabled platform controller capabilities |

The raw compatibility path documents the equivalent opt-in patch in
`manifests/platform-rbac.yaml`.

## Token rotation

The chart never creates the token Secret; it references your
`fortigate.existingSecret`. Kubernetes does not restart pods when a Secret
changes, so after rotating the token:

```sh
kubectl -n <namespace> rollout restart deployment/fortigate-external-dns
```

Alternatively, annotate the pod for reloader-style controllers (for example
[stakater/Reloader](https://github.com/stakater/Reloader)) via `podAnnotations`.

For target-mode references, rotate one Secret or CA object at a time and verify
the target before revoking old material. Credentials remain in memory; the next
resync re-resolves references and rebuilds only the affected target client, so a
pod restart is not required. The rollout above applies to direct single-target
mode.

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
`--fortigate-ca-file`. A checksum on the Pod template automatically rolls the
Deployment when the bundle changes. It replaces the system roots for FortiGate
connections; TLS 1.2 is the enforced minimum.

## Egress containment

The controller holds a firewall-admin token. `egressNetworkPolicy` (opt-in)
denies all egress except DNS, the Kubernetes API, and the FortiGate endpoint:

```yaml
egressNetworkPolicy:
  enabled: true
  fortigate:
    cidr: 203.0.113.10/32   # required when enabled
    port: 443
  kubeAPI:
    cidr: 10.96.0.1/32      # required when enabled
    ports: [443, 6443]
  dns:
    enabled: true
    cidr: 10.96.0.10/32     # required while DNS egress is enabled
```

All enabled peers must have explicit CIDRs and the Kubernetes API list must
contain at least one port. Empty values fail rendering instead of opening an
all-destination or all-port rule.

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
| `serviceAccount.name` | `""` | Existing ServiceAccount name; required when `create=false` (there is no `default` ServiceAccount fallback). |
| `rbac.create` | `true` | Create all required RBAC (source reads and, with leader election, the Lease Role). When `false` you must provide every grant yourself. |
| `sources` | `[service, ingress, gateway]` | Enabled discovery sources. |
| `namespaces` | `[]` | Namespaces to watch. Empty means all namespaces (cluster-scoped RBAC). |
| `gatewayTargetNamespaces` | `[]` | Extra namespaces consulted only to resolve parent Gateway addresses; read-only, no cleanup ownership. |
| `domainFilters` | `[]` | Domain suffixes to include. Scope this tightly per cluster. |
| `ownerID` | `fortigate-external-dns` | In-process diagnostic identity. It is not persisted in FortiGate record comments. |
| `defaultTTL` | `300` | Default record TTL (seconds). |
| `dryRun` | `true` | **Default on**: log the plan, write nothing. Set `false` to enable writes. |
| `once` | `false` | Run one reconcile loop and exit. |
| `interval` | `1m` | Reconciliation interval; use positive Go-duration integer components such as `90s` or `1m30s`. |
| `reconcileTimeout` | `2m` | Per-loop timeout; use positive Go-duration integer components. |
| `cleanupPolicy` | `delete` | Stale managed-record handling: `delete` (destructive), `deactivate`, or `keep`. |
| `allowEmptyDesiredCleanup` | `false` | Permit cleanup when a successful discovery finds zero desired endpoints. Leave off except for intentional decommissioning — it is the mass-delete misconfiguration guard. |
| `maxCleanupPerCycle` | `0` | Refuse a cycle's cleanup when more than this many delete/deactivate operations are planned (`0` = unlimited). |
| `logFormat` | `text` | Log output format: `text` or `json`. |
| `logLevel` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `healthzMaxStaleness` | `""` | Liveness heartbeat window using integer duration components. Empty derives `max(5*interval, 5m)`. |
| `leaderElection.enabled` | `true` | Lease-based single-writer election. |
| `leaderElection.id` | `""` | Lease name; defaults to the chart fullname. |
| `leaderElection.namespace` | `""` | Lease namespace; defaults to the release namespace. |
| `metrics.enabled` | `true` | Gates scrape exposure only (Service/NetworkPolicy). Probes always render; the server always binds `metrics.port`. |
| `metrics.port` | `8080` | Pod port serving `/healthz`, `/readyz`, `/metrics`. |
| `metrics.service.enabled` | `false` | Render a ClusterIP Service for scraping. |
| `metrics.service.annotations` | `{}` | Metrics Service annotations. |
| `metrics.networkPolicy.enabled` | `false` | Ingress NetworkPolicy for the metrics port (deny-by-default; see values comment about kubelet probes). |
| `metrics.networkPolicy.allowedNamespaces` | `[]` | Namespace label selectors allowed to scrape. |
| `fortigate.url` | `""` (required) | FortiGate API base URL (`https://...`), without userinfo, query parameters, or a fragment. |
| `fortigate.zone` | `""` (required) | Existing `system dns-database` zone to manage records in. |
| `fortigate.vdom` | `root` | FortiGate VDOM. |
| `fortigate.existingSecret` | `""` (required) | Secret containing the API token. The chart never creates it. |
| `fortigate.apiTokenSecretKey` | `api-token` | Key inside the Secret. |
| `fortigate.exclusiveZoneOwnership` | `false` | Required acknowledgement before writes. Confirms the entire DNS database is exclusive to this controller. |
| `fortigate.insecureSkipVerify` | `false` | Disable TLS verification. Prefer `caBundle`; mutually exclusive with it. |
| `fortigate.caBundle` | `""` | Inline PEM CA chain used instead of system roots to verify the device. |
| `fortigate.timeout` | `15s` | FortiGate API request timeout using positive integer duration components. |
| `fortigate.retries` | `2` | Retry count for retryable FortiGate failures (0–10). |
| `egressNetworkPolicy.enabled` | `false` | Opt-in deny-all egress with allowlist (DNS, kube API, FortiGate). |
| `egressNetworkPolicy.fortigate.cidr` | `""` | FortiGate management CIDR (required when enabled). |
| `egressNetworkPolicy.fortigate.port` | `443` | FortiGate API port. |
| `egressNetworkPolicy.kubeAPI.cidr` | `""` | Kubernetes API CIDR (required when enabled). |
| `egressNetworkPolicy.kubeAPI.ports` | `[443, 6443]` | Ports allowed toward the API server. |
| `egressNetworkPolicy.dns.enabled` | `true` | Allow UDP/TCP 53 egress. |
| `egressNetworkPolicy.dns.cidr` | `""` | Resolver CIDR (required while DNS egress is enabled). |
| `platform.targetMode.enabled` | `false` | Activate chart-managed target CRs, platform RBAC, and the target-mode runtime instead of direct connection arguments. |
| `platform.targetMode.targets` | `[]` | Bounded target CR definitions; all credentials are key references. |
| `platform.targetMode.apiTokenSecretNames` | `[]` | API-token Secret names allowed by resourceName-bound RBAC. |
| `platform.targetMode.caSecretNames` / `caConfigMapNames` | `[]` | CA object names allowed by resourceName-bound RBAC. |
| `platform.sharedOwnership.enabled` | `false` | Enable shared-ownership targets and ownership-claim gates/RBAC. |
| `platform.planApproval.enabled` | `false` | Enable change-plan approval RBAC; required by targets using approval mode. |
| `platform.planApproval.retention` | `20` | Bounded completed-plan retention (1–100). |
| `platform.policy.enabled` | `false` | Enable policy CRs, enforcement, and read RBAC. |
| `platform.events.enabled` | `false` | Enable event/workqueue watches, debounce/resync flags, and rollout checksum. |
| `platform.events.debounce` / `resync` | `2s` / `1m` | Positive workqueue debounce and periodic full-audit/credential-rotation durations. |
| `platform.sourceExpansion.externalName.enabled` | `false` | Enable opted-in ExternalName CNAME source expansion. |
| `platform.sourceExpansion.headless.enabled` | `false` | Enable opted-in headless/EndpointSlice A/AAAA expansion and its RBAC. |
| `platform.status.enabled` / `retention` | `false` / `20` | Enable status-resource RBAC with bounded history retention (1–100). |
| `monitoring.grafanaDashboard.enabled` | `false` | Render a Grafana dashboard ConfigMap. |
| `monitoring.prometheusRule.enabled` | `false` | Render PrometheusRule-compatible alerts; requires that CRD in the cluster. |
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
  --reuse-values \
  --set-json 'sources=["service","ingress","gateway"]' \
  --set-json 'namespaces=[]' \
  --set fortigate.exclusiveZoneOwnership=true \
  --set allowEmptyDesiredCleanup=true --set once=true --set dryRun=false
```

Run this only when all configured source APIs are available and the exclusive
zone should become empty, then uninstall the release. Without
`allowEmptyDesiredCleanup=true` the controller refuses a cycle that would
delete every record.

For shared mode, stop writes and desired sources first. Do not
delete ownership, plan, target, or status CRDs/finalizers until provider records
have been intentionally retained or removed and absence is verified. Back up
metadata without credential Secret contents:

```sh
kubectl get fortigatednstargets,fortigatednsrecordownerships,fortigatednschangeplans,fortigatednsstatuses \
  -A -o yaml > platform-backup.yaml
```

If CRDs or claims are lost, stop every writer, reinstall the APIs, restore a
known-good backup, and re-list FortiGate before reconfirming exact IDs and
fingerprints. Missing CRs never authorize provider deletion, and operators must
not recreate `Confirmed` status by hand. Plan/status retention is bounded to
1–100 entries (default 20); terminal audit history is pruned, while pending,
approved, applying, and interrupted plans are preserved.
