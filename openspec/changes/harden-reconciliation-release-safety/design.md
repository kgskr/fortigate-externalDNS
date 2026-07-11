## Context

FortiOS documents no free-form ownership field for `system dns-database dns-entry`, while the controller currently writes and reads a `comment` property as its only ownership boundary. The reconciliation planner also emits same-cycle create and cleanup operations for non-1:1 target transitions, source discovery can be partial without constraining cleanup, and FortiGate collection responses can be truncated. These correctness risks combine with deployment fail-open defaults and validation drift.

The project is pre-1.0, the Helm chart starts in dry-run mode, and safety is more important than preserving an undocumented shared-zone behavior. A fully shared DNS zone would require a durable ownership registry with conflict-safe updates; a ConfigMap-sized JSON registry is not suitable as the general design because it is size-bounded and would become another single-writer data store.

## Goals / Non-Goals

**Goals:**

- Make every destructive mutation depend on complete source and provider state.
- Use only documented FortiGate record fields and make ownership assumptions explicit.
- Preserve independent-operation progress while preventing cleanup after prerequisite failures.
- Keep ordinary clusters without Gateway API usable while freezing unsafe cleanup.
- Align the chart, toolchain, scripts, documentation, and OpenSpec validation with runtime behavior.

**Non-Goals:**

- Supporting multiple controllers or manual records inside the same managed FortiGate DNS database.
- Adding a CRD-backed shared-zone ownership registry in this change.
- Discovering Kubernetes API or DNS resolver CIDRs automatically for NetworkPolicy.

## Decisions

### Exclusive-zone ownership is the only write mode

The client will stop sending and parsing the undocumented `dns-entry.comment` property. A new `--fortigate-exclusive-zone-ownership` / `FORTIGATE_EXCLUSIVE_ZONE_OWNERSHIP` acknowledgement is required whenever dry-run is disabled. With complete, unrestricted discovery, every record returned from the configured FortiGate DNS database is treated as owned by this controller.

Destructive cleanup or mutation of an existing mismatched row with source or namespace restrictions cannot be proven safe without persisted per-record source metadata, so validation will require `cleanup-policy=keep` for restricted source/namespace configurations. In that restricted mode only a current row that exactly matches a desired key, TTL, and status is recognized as owned; a target, type, TTL, or status change fails closed as a conflict, while a genuinely missing name can still be created. Unrestricted, exclusive-zone deployments can use full update, replace, delete, or deactivate behavior. Domain filters remain enforceable from the record hostname.

Alternative considered: keep using `comment` after a live `action=schema` check. This was rejected because it still leaves portability and upgrade behavior dependent on an undocumented device field. A CRD registry is the future path for shared-zone coexistence.

### Replacement is converged in two phases

The planner will keep the existing in-place `Replace` for a safe 1:1 target change, including a 1:1 A/AAAA-to-CNAME type transition using the existing provider ID. Multi-row CNAME type transitions and desired sets that combine CNAME with address records fail closed as planning conflicts. If a compatible logical record has any missing desired targets, cleanup of stale targets for that DNS name is withheld for the cycle. The next reconcile deletes or deactivates stale targets only after all desired targets are observable. The apply layer will additionally skip same-name cleanup if a preceding create, update, or replace fails, while continuing independent names.

This avoids introducing a transaction protocol and is safe even when a create response is lost after the device committed the record.

### FortiGate collection must be complete and stable

List responses will parse `limit_reached`, `matched_count`, `next_idx`, and `revision`. The client requests a fixed page size and, when limited, requests the next page using `start=next_idx+1` because FortiGate reports the last returned index. It rejects missing metadata, non-advancing cursors, duplicate provider IDs, revision changes across pages, and a terminal collected size smaller than `matched_count` rather than passing an incomplete snapshot to the planner.

### Source incompleteness freezes cleanup

Discovery results will carry incomplete source names. Missing or gone Gateway API resources mark the Gateway source incomplete while allowing Service and Ingress desired records to be created or updated. Any incomplete configured source suppresses cleanup for the cycle; this is intentionally conservative because exclusive-zone records no longer carry a persisted source identity.

### Gateway targets are typed before DNS conversion

Gateway `IPAddress` values must parse as IP addresses, `Hostname` values must be non-IP hostnames, and custom address types are ignored with an observable event. Nil address type follows the Gateway API default of `IPAddress`. Across all accepted parents, hostname targets win over IP targets so one DNS name never receives both CNAME and address records.

### Long-running reconciliation survives transient startup failure

`Runner.Run` will log the first failed attempt and continue on its interval. `--once` keeps returning the first error. Cancellation still exits promptly.

### Deployment inputs fail closed

- FortiGate URLs containing userinfo, query parameters, or fragments are rejected, never rendered as help defaults, and log redaction remains defensive.
- Enabling egress NetworkPolicy requires explicit FortiGate, Kubernetes API, and enabled-DNS CIDRs plus at least one API port.
- The CA bundle hash is attached to the Pod template so a Helm upgrade rolls the Deployment.
- Positive runtime durations are enforced by the values schema.
- Secret scanning recognizes quoted YAML/JSON credential keys.
- Go source and builder inputs move together to 1.26.5, with the builder image pinned to its verified multi-architecture digest.
- OpenSpec baseline validation becomes a normal validation target, and requirement prose is kept parser-safe.

## Risks / Trade-offs

- **Breaking ownership migration**: existing comment-managed shared zones cannot be safely upgraded in write mode. → Keep Helm dry-run as the default, require explicit exclusive-zone acknowledgement, and document migration before writes are enabled.
- **Cleanup pauses when Gateway API is absent**: default source configuration includes Gateway. → Continue safe creates/updates and emit an incomplete-source diagnostic; install the required CRDs for full unrestricted cleanup, or disable the unused source and use the restricted `cleanup-policy=keep` mode.
- **Two-phase replacement delays stale cleanup by one interval**: old and new targets coexist temporarily. → Prefer temporary overlap over deleting the last known-good target; 1:1 replacement remains in-place.
- **Pagination can fail during concurrent device changes**: revision mismatch aborts a cycle. → Retry from a fresh snapshot on the next reconcile instead of planning from mixed revisions.
- **Explicit NetworkPolicy CIDRs add installation work**: cluster-specific peers cannot be inferred portably. → Keep the policy opt-in and provide complete examples.

## Migration Plan

1. Upgrade while `dryRun=true` and inspect the exclusive-zone plan.
2. Ensure the configured FortiGate DNS database contains no records owned by another system or operator.
3. If source or namespace filters are used, select `cleanup-policy=keep`; otherwise remove those restrictions for full exclusive-zone reconciliation.
4. Set `fortigate.exclusiveZoneOwnership=true`, then enable writes.
5. When egress NetworkPolicy is enabled, provide all required CIDRs before upgrading.

Rollback restores the prior image while leaving the Helm release in dry-run mode. Records created without comments remain ordinary FortiGate records; the previous release must not resume writes against them because it cannot prove their old comment ownership.

## Open Questions

- A future shared-zone mode should use a dedicated CRD registry with provider ID, normalized record fingerprint, source identity, resource version conflict handling, and explicit adoption/migration semantics.
