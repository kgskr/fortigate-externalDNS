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

## Upgrading the CRDs

Helm installs the files in `crds/` only on first install and never upgrades or
deletes them, and the CRDs do change between releases (for example v0.3.1 made
the target URL pattern https-only). Before every `helm upgrade` to a new chart
version, apply the CRD file from the matching release tag server-side:

```sh
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/kgskr/fortigate-externalDNS/v<version>/charts/fortigate-external-dns/crds/fortigate-external-dns.yaml
helm upgrade fortigate-external-dns oci://ghcr.io/kgskr/charts/fortigate-external-dns --version <version> --reuse-values
```

(`manifests/crds/fortigate-external-dns.yaml` in the same tag is a
byte-identical copy.) Server-side apply avoids the client-side annotation size
limit. Compare with `kubectl diff --server-side -f <file>` first if you want to
review the schema change.

### Minimum Kubernetes version

`FortiGateDNSPolicy.spec.allowedTargetCIDRs` entries are validated with the CEL
`isCIDR()` function (`x-kubernetes-validations`), which needs Kubernetes 1.31+
(the CEL IP/CIDR library; available behind a beta gate from 1.30). CRDs already
served, `default:` values, and the remaining schema need only ordinary
`apiextensions.k8s.io/v1`. `FortiGateDNSTarget.spec.retries` defaults to `2` (the
same as direct mode); set `retries: 0` explicitly to disable retries.

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
| Cluster scope, restricted to source namespace names when configured | core `namespaces` | `get` only | Gateway listener `AllowedRoutes` namespace selectors |
| Leader-election namespace | `coordination.k8s.io/leases` | `create`; `get,update` restricted to the configured Lease name | `leaderElection.enabled` |
| Release namespace | `fortigatednstargets` | `get,list`; add `watch` with `platform.events.enabled` | Target mode |
| Release namespace | `fortigatednsrecordownerships` | `get,list,create,update,delete`; add `watch` with events; `/status`: `update`; `/finalizers`: `update` | Shared ownership (claims are deleted after verified provider removal and carry a finalizer) |
| Release namespace | `fortigatednschangeplans` | `get,list,create,delete`; add `watch` with events; `/status`: `update` | Plan approval |
| Release namespace | `fortigatednsstatuses` | `get,create`; `/status`: `update` | Target mode (the status writer always runs) or `platform.status.enabled` |
| Source namespace(s), or cluster-wide when `namespaces=[]` | `fortigatednspolicies` | `get,list`; add `watch` with events | Policy enforcement |
| Release namespace | named core `secrets` / `configmaps` | `get`, restricted with `resourceNames` | Every referenced token/CA object |

`fortigatednspolicies` are listed in the **source** namespaces (the same scope
as source discovery), not the release namespace. `watch` is granted only when
`platform.events.enabled=true`, because informers exist only in event-driven
mode. The controller creates no Kubernetes `Event` objects, uses no `patch`,
never deletes status resources, and writes target status only through
`fortigatednsstatuses`, so none of those grants are rendered.

HTTPRoute publication checks listener protocol, allowed Route kinds, and
namespace rules before expanding hostnames. Namespace selector labels are read
once per namespace per discovery pass and refreshed on the next pass (including
periodic audits). If that lookup fails, affected Routes are not published and
cleanup is suppressed. Gateway-enabled installs include the read-only namespace
grant above; upgrades with custom RBAC must add it when selectors are used.

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
denies all egress except DNS, the Kubernetes API, and the FortiGate endpoint(s):

```yaml
egressNetworkPolicy:
  enabled: true
  fortigate:
    cidr: 203.0.113.10/32            # legacy single peer, still supported
    cidrs: [198.51.100.20/32]        # add more for multi-target mode
    ports: [443]                     # optional; replaces `port`
  kubeAPI:
    cidrs: [192.0.2.10/32]           # API server endpoint IPs, not the ClusterIP
    ports: [443, 6443]
  dns:
    enabled: true
    namespaceSelector:
      matchLabels: {kubernetes.io/metadata.name: kube-system}
    podSelector:
      matchLabels: {k8s-app: kube-dns}
```

Do not put Service ClusterIPs (for example `10.96.0.1` for
`kubernetes.default` or `10.96.0.10` for kube-dns) in an `ipBlock`: most CNIs
evaluate egress rules after Service DNAT, so the real endpoint address is what
must be allowed. Use the API server endpoint IPs
(`kubectl get endpoints kubernetes`) for `kubeAPI`, and the namespace/pod
selector form for cluster DNS (`dns.cidr`/`cidrs` remains for an external
resolver). All enabled peers must be explicit, and the Kubernetes API port
list must be non-empty; missing values fail rendering instead of opening an
all-destination or all-port rule.

## Health probing and metrics

The probe/metrics HTTP server always runs on `metrics.port`; liveness and
readiness probes are always rendered. `metrics.enabled` gates only scrape
exposure (the metrics Service and its ingress NetworkPolicy). Liveness fails
when the reconciling replica completes no reconcile attempt within the
heartbeat window (`healthzMaxStaleness`, default `max(5*interval, 5m)`) — a
wedged loop restarts, while a reachable-but-erroring FortiGate does not.

### Monitoring

`monitoring.serviceMonitor.enabled=true` renders a prometheus-operator
`ServiceMonitor` for the metrics Service (`metrics.service.enabled=true` is
required); kube-prometheus-stack ignores `prometheus.io/scrape` annotations, so
set `monitoring.serviceMonitor.labels` to your Prometheus selector, as in
[monitoring-values.yaml](../../samples/monitoring-values.yaml). The
`ReconcileStale` alert uses
`time() - max by (namespace) (...last_successful_reconcile_timestamp_seconds{...})`
so idle leader-election standby replicas (which export 0) do not fire it; only
the most recent success across the release counts. A
`FortiGateExternalDNSMetricsAbsent` alert fires when `build_info` disappears
(controller down or not scraped). The alert selectors assume the ServiceMonitor
`job` label (`<fullname>-metrics`); adjust the rule if you scrape differently.

## Writers, leader election, and one-shot runs

The FortiGate DNS database admits exactly one writer. The chart therefore
fails to render `replicaCount>1` with `leaderElection.enabled=false`. With
`once=true` the chart renders a Job instead of a Deployment (one pod, no leader
election, no probes, `backoffLimit: 0`), named with the Helm revision so
upgrades create a fresh Job instead of patching an immutable one. Do not run a
one-shot Job while another release is writing to the same database.

## Values

| Key | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Controller replicas. Values above 1 are safe only with `leaderElection.enabled=true` (Lease-based single writer); the render fails otherwise. Ignored when `once=true`. |
| `priorityClassName` | `""` | Pod `priorityClassName`. |
| `podDisruptionBudget.enabled` | `false` | Render a PodDisruptionBudget. Only valid with `replicaCount>1`, `leaderElection.enabled=true`, and `once=false`; otherwise rendering fails (a PDB on one replica blocks node drains). |
| `podDisruptionBudget.minAvailable` / `maxUnavailable` | `1` / `null` | PDB budget; `maxUnavailable` takes precedence when set. |
| `topologySpreadConstraints` | `[]` | Passed through to the pod spec; include your own `labelSelector`. |
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
| `once` | `false` | Render a `batch/v1` Job (`restartPolicy: Never`, `backoffLimit: 0`, no probes) instead of a Deployment. It is named `<fullname>-r<revision>` so every `helm upgrade` creates a new Job. Runs one reconcile and exits, without leader election. |
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
| `egressNetworkPolicy.fortigate.cidr` / `cidrs` | `""` / `[]` | FortiGate management CIDR(s). The single `cidr` and the `cidrs` list are merged; at least one is required. Use the list for several FortiGates in multi-target mode. |
| `egressNetworkPolicy.fortigate.port` / `ports` | `443` / `[]` | FortiGate API port; a non-empty `ports` list replaces `port`. |
| `egressNetworkPolicy.kubeAPI.cidr` / `cidrs` | `""` / `[]` | Kubernetes API server endpoint IP CIDR(s), not the ClusterIP (required when enabled). |
| `egressNetworkPolicy.kubeAPI.ports` | `[443, 6443]` | Ports allowed toward the API server. |
| `egressNetworkPolicy.dns.enabled` | `true` | Allow UDP/TCP 53 egress. |
| `egressNetworkPolicy.dns.cidr` / `cidrs` | `""` / `[]` | Resolver ipBlock(s). Optional when a selector is given. |
| `egressNetworkPolicy.dns.namespaceSelector` / `podSelector` | `{}` | Select in-cluster DNS pods (for example kube-dns) instead of an ipBlock; both set means one peer matching both. |
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
| `monitoring.serviceMonitor.enabled` | `false` | Render a prometheus-operator ServiceMonitor for the metrics Service; requires `metrics.service.enabled=true` and the CRD. |
| `monitoring.serviceMonitor.namespace` / `labels` | `""` / `{}` | ServiceMonitor namespace (default: release namespace) and the labels your Prometheus selects on. |
| `monitoring.serviceMonitor.interval` / `scrapeTimeout` | `30s` / `10s` | Scrape settings. |
| `monitoring.grafanaDashboard.enabled` | `false` | Render a Grafana dashboard ConfigMap. |
| `monitoring.prometheusRule.enabled` | `false` | Render PrometheusRule-compatible alerts; requires that CRD in the cluster. Alerts select series by `namespace` and `job="<fullname>-metrics"` (the ServiceMonitor default). |
| `podAnnotations` / `podLabels` | `{}` | Extra pod metadata (e.g. reloader annotations). |
| `resources` | requests `25m/64Mi`, limits `200m/128Mi` | Container resources. |
| `nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | Scheduling controls (see also `topologySpreadConstraints`, `priorityClassName`, `podDisruptionBudget`). |
| `securityContext` | restricted-PSS-compliant | Container security context (non-root 65532, no privilege escalation, read-only rootfs, drop ALL). |
| `podSecurityContext` | `fsGroup: 65532`, `RuntimeDefault` seccomp | Pod security context. |

## Decommissioning

To intentionally remove all managed records (for example when retiring a
cluster), run one final cycle with the empty-desired guard overridden:

`once=true` replaces the Deployment with a one-shot Job, and Helm creates the
Job before it deletes the old Deployment. Stop the running writer first so two
writers never overlap:

```sh
kubectl -n <namespace> scale deployment/fortigate-external-dns --replicas=0
kubectl -n <namespace> wait --for=delete pod -l app.kubernetes.io/instance=fortigate-external-dns --timeout=2m
helm upgrade fortigate-external-dns ... \
  --reuse-values \
  --set-json 'sources=["service","ingress","gateway"]' \
  --set-json 'namespaces=[]' \
  --set fortigate.exclusiveZoneOwnership=true \
  --set allowEmptyDesiredCleanup=true --set once=true --set dryRun=false
kubectl -n <namespace> wait --for=condition=complete job -l app.kubernetes.io/instance=fortigate-external-dns --timeout=10m
kubectl -n <namespace> logs job -l app.kubernetes.io/instance=fortigate-external-dns
```

The Job runs a single cleanup reconcile (`backoffLimit: 0`; a failure is not
retried automatically). Run this only when all configured source APIs are available and the exclusive
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
