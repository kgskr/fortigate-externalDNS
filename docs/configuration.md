# Configuration reference

[README](../README.md) · [Helm values](../charts/fortigate-external-dns/README.md)

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

## Operability flags

| Flag | Env | Default | Purpose |
| --- | --- | --- | --- |
| `--cleanup-policy` | `CLEANUP_POLICY` | `delete` | What to do with stale records in the exclusive database: `delete` (destructive), `deactivate` (disable but retain), or `keep` (never remove). Restricted sources or namespaces require `keep`. |
| `--allow-empty-desired-cleanup` | `ALLOW_EMPTY_DESIRED_CLEANUP` | `false` | Mass-cleanup guard override. By default, a cycle whose *successful* discovery finds zero desired endpoints refuses all cleanup — that state is the signature of a misconfiguration (wrong `--domain-filter` or `--namespace`), not a teardown. Enable only for intentional decommissioning. |
| `--max-cleanup-per-cycle` | `MAX_CLEANUP_PER_CYCLE` | `0` | Refuses a cycle's cleanup when more than this many delete/deactivate operations are planned (`0` = unlimited). Creates and updates still apply; refusals are logged at error level and counted in `cleanup_refused_total`. |
| `--reconcile-timeout` | `RECONCILE_TIMEOUT` | `2m` | Bounds each reconcile loop, including Kubernetes list and FortiGate calls. |
| `--interval` | `INTERVAL` | `1m` | Reconciliation interval between loops. |
| `--default-ttl` | `DEFAULT_TTL` | `300` | Default DNS record TTL in seconds when a source does not specify one. |
| `--fortigate-timeout` | `FORTIGATE_TIMEOUT` | `15s` | Timeout for each FortiGate API request. |
| `--fortigate-retries` | `FORTIGATE_RETRIES` | `2` | Retry count for retryable FortiGate API failures. |
| `--leader-election` | `LEADER_ELECTION` | `true` | Lease-based single-writer guard for multi-replica deployments. Ignored with `--once`. |
| `--leader-election-id` | `LEADER_ELECTION_ID` | `fortigate-external-dns` | Lease name. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | pod namespace | Namespace for the Lease. |
| `--metrics-addr` | `METRICS_ADDR` | `:8080` | Bind address for `/healthz`, `/readyz`, and `/metrics`. Empty disables the server (and with it the probes). |
| `--healthz-max-staleness` | `HEALTHZ_MAX_STALENESS` | `0` (auto) | Liveness heartbeat window: while this replica is responsible for reconciling (leader, or leader election disabled), `/healthz` fails once no reconcile attempt has *completed* within the window, so a wedged loop is restarted. Attempts that fail still count — a FortiGate outage does not restart the pod. `0` derives `max(5×interval, 5m)`, or `max(5×max(interval, resync), 5m)` in target mode. |
| `--fortigate-ca-file` | `FORTIGATE_CA_FILE` | (none) | Path to a PEM CA bundle used *instead of* system roots to verify the FortiGate TLS certificate — the right way to trust a private-CA device. Mutually exclusive with `--fortigate-insecure-skip-verify` (setting both fails validation). TLS 1.2 is the enforced minimum either way. |
| `--fortigate-exclusive-zone-ownership` | `FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP` | `false` | Required acknowledgement before writes are enabled. Confirms every record in the configured FortiGate DNS database is exclusively managed by this controller; shared/manual records are unsupported. Restricted sources or namespaces require `cleanup-policy=keep`. |
| `--log-format` | `LOG_FORMAT` | `text` | Log output format: `text` or `json` (for log aggregation pipelines). |
| `--log-level` | `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `--version` | — | — | Print the stamped version and commit, then exit. |
| `--gateway-target-namespace` | `GATEWAY_TARGET_NAMESPACES` | (none) | Extra namespaces consulted only to resolve parent Gateway addresses. Lookup scope only; does not expand ownership or cleanup. In namespaced installs the Helm chart auto-renders a read-only `gateways` Role in each of these namespaces. |
| `--plan-output` | `PLAN_OUTPUT` | (none) | With `--once`, atomically write the canonical, credential-free reconciliation plan for review. Refuses an existing path unless overwrite is explicitly allowed. |
| `--plan-output-overwrite` | `PLAN_OUTPUT_OVERWRITE` | `false` | With `--once --plan-output`, explicitly allow replacing an existing plan file. |
| `--approved-plan-hash` | `APPROVED_PLAN_HASH` | (none) | With `--once`, apply only when the lowercase SHA-256 exactly matches the newly generated canonical plan; provider, source, policy, and ownership state are rebuilt and revalidated immediately before apply. |
| `--target-mode` | `TARGET_MODE` | `false` | Load namespaced `FortiGateDNSTarget` resources instead of direct FortiGate flags; the modes are mutually exclusive. |
| `--platform-namespace` | `PLATFORM_NAMESPACE` | pod namespace | Namespace containing target, claim, plan, and status resources. `FortiGateDNSPolicy` resources are read from the *source* namespaces (`--namespace`, or all namespaces when unset), not from the platform namespace. |
| `--policy-enforcement` | `POLICY_ENFORCEMENT` | `false` | Evaluate matching `FortiGateDNSPolicy` resources before planning. |
| `--event-driven` | `EVENT_DRIVEN` | `false` | Enable target-mode informer/workqueue reconciliation; periodic `--resync` remains the full-audit and credential-rotation boundary. |
| `--debounce` / `--resync` | `DEBOUNCE` / `RESYNC` | `2s` / `1m` | Bound semantic event coalescing and periodic full audit. |
| `--status-retention` | `STATUS_RETENTION` | `20` | Keep 1–100 per-target status/audit entries. |
| `--plan-retention` | `PLAN_RETENTION` | `20` | Keep 1–100 completed change plans, independently of status/audit retention. |
| `--publish-external-name-services` | `PUBLISH_EXTERNAL_NAME_SERVICES` | `false` | Permit target- and policy-authorized ExternalName CNAME publication. |
| `--publish-headless-services` | `PUBLISH_HEADLESS_SERVICES` | `false` | Permit opted-in headless Service A/AAAA publication from EndpointSlices. |

Metrics are exposed in Prometheus text format under the `fortigate_external_dns_`
prefix (reconcile counters, a reconcile duration histogram, operation counters
labelled by type and result — `planned`, `applied`, `failed`, `skipped`,
`conflict` — a last-successful-reconcile timestamp, a `cleanup_refused_total`
counter for mass-cleanup guard trips, and a `build_info` gauge carrying the
version/commit). No tokens or record payloads are exposed.

Target mode populates platform metric families for target health, queue depth,
policy denial, ownership/adoption, plan phase, and audit state. Metrics remain
credential-free and target failures are reported independently.
