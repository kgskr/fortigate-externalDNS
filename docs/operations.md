# Operations

[README](../README.md) · [Helm values](../charts/fortigate-external-dns/README.md)

## Safety invariants

- A stable, complete provider revision is required before cleanup or approval.
- Discovery rejects an entire source object before endpoint allocation when its
  hostname/target product exceeds 1,024 endpoints, or when the reconciliation
  total would exceed 10,000. The source is marked incomplete so cleanup stops.
- Dry-run never mutates FortiGate or fabricates ownership confirmation.
- Shared mutation requires an exact `Confirmed` claim; CRD loss never implies
  provider deletion.
- Shared adoption and target/type replacement are rejected by the current
  runtime. Stop controller writes and use an audited operator process; preserve
  claims/finalizers, never invent a source UID, and never write
  `status.phase=Confirmed`.
- An approval is not reusable after discovery, policy, ownership, target, or
  provider state changes.
- Overlapping write-enabled targets are invalid unless both are
  non-destructive (`cleanupPolicy=keep`) and explicitly allow overlap. An
  invalid or overlapping target is excluded and reported in its status while
  healthy targets keep reconciling.
- A `FortiGateDNSPolicy` that fails to validate denies publication in its own
  namespace and suppresses all cleanup until it is fixed; other namespaces
  keep publishing.

## Decommissioning a cluster's records

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

## Migration and recovery

> **Safety gate:** platform features are disabled by default. Keep every new
> target in dry-run with `cleanupPolicy=keep` until backup, overlap, policy,
> claim, approval, and rollback checks below pass. One Deployment per exclusive
> target remains a supported isolation alternative.

### Exclusive to shared ownership

1. Keep the new target in dry-run with `cleanupPolicy=keep`; stop changes to the
   old controller and take an external FortiGate DNS database backup.
2. Back up Kubernetes metadata without Secret contents:
   `kubectl get fortigatednstargets,fortigatednsrecordownerships,fortigatednschangeplans,fortigatednsstatuses -A -o yaml > platform-backup.yaml`.
3. Review every provider row. Existing unowned rows cannot be adopted by the
   current runtime; keep writes stopped and migrate them through an audited
   operator process.
4. Preserve claims and finalizers throughout migration. Never invent a source
   UID or patch claim status. A new claim must be reserved from a currently
   observed Kubernetes object with its real API version and UID.
5. Enable writes only after every record that can be mutated has a confirmed
   claim and a fresh dry-run shows no conflicts.
   Target or record-type replacement is unsupported by the runtime; stop writes
   and use the operator process above. A prior claim cannot authorize the new
   record identity.
6. Never run the old exclusive controller against the now-shared database. For
   rollback, disable writes first, preserve claims/finalizers, inspect FortiGate
   state, and restore the former exclusive database/controller only after the
   shared controller is stopped.

The illustrative adoption and approval CRs in [samples](../samples/) are review aids, not
objects to copy into production. The controller must generate their exact
fingerprints, revisions, canonical document, and hash.

### Legacy to multi-target

Create one dry-run `FortiGateDNSTarget` per existing Deployment, using only
Secret/CA key references. Preserve each old source, namespace, domain, VDOM,
zone, cleanup, and controller identity boundary. Validate that writable DNS
scopes do not overlap; dry-run targets do not count as writers, while an
intentional non-destructive overlap requires `cleanupPolicy=keep` and
`allowNonDestructiveOverlap=true` on both targets. Review targets independently
and enable one at a time so one target's auth, TLS, API, or policy failure cannot
authorize changes on another.

Rotate token and CA objects one target at a time, wait for that target to become
healthy, then revoke the old material. Target mode holds credentials only in
memory, re-resolves references on resync, and rebuilds only the affected target
client; no pod restart is required. The direct single-target chart path still
requires `kubectl rollout restart deployment/<name>` after Secret rotation
(inline `fortigate.caBundle` changes already roll the pod). One Deployment,
ServiceAccount, credential Secret, and exclusive database per target remains a
supported operational alternative.

### Decommissioning and disaster recovery

For an exclusive target, use the guarded final cycle documented above, verify
FortiGate state, then uninstall. For shared mode, stop writes and remove desired
sources first; do not delete claim/plan/target CRDs or
their finalizers before provider records have been intentionally retained or
removed and absence is verified.

If platform CRDs are lost, stop all writers. Do not interpret missing claims as
permission to delete and do not recreate `Confirmed` status by hand. Restore
the API definitions and a known-good metadata backup, take a fresh FortiGate
snapshot, and let the runtime revalidate exact provider IDs/fingerprints. Any
uncertain row stays orphaned/conflicted until reviewed. Status and completed
plan history are bounded to 1–100 entries (chart default 20); pending, approved,
applying, and interrupted plans are not pruned as completed audit history.

### Troubleshooting

| Symptom | Check / response |
| --- | --- |
| Dry-run plans unexpected mass cleanup | Verify source APIs, `domainFilters`, namespaces, and zone; leave the empty-desired override off. |
| Approval hash rejected | Regenerate the plan; exact canonical bytes or a precondition changed. Use a lowercase 64-character SHA-256. |
| Target/policy/claim CR exists but nothing happens | Verify `platform.targetMode.enabled`, namespace/RBAC, target status conditions, policy selectors, and exact plan approval. |
| Shared claim is not `Confirmed` | Do not enable writes; inspect conflict, provider revision, ID/fingerprint, and approval state. |
| One proposed target overlaps another | Separate zones/domains, or keep both non-destructive and explicitly acknowledge overlap. |
| Token/CA rotation causes authentication or TLS failure | Restore the previous referenced object, isolate that target, then rotate and verify before revocation. |
| CRDs or claims disappeared | Stop writers and follow the disaster-recovery procedure; never assume provider ownership from absence. |
