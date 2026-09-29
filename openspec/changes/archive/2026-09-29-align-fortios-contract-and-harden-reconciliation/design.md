## Context

The controller had only been checked against Fortinet's published documentation and fake HTTP servers. The first live run was a targeted create, resolve, and delete of test records on a FortiOS v7.2.11 device whose zone also held hand-managed records, plus read-only list checks. It showed that the name encoding and list pagination assumptions were wrong. A review of the reconciliation core, sources, target runtime, and deployment artifacts then found safety and operability gaps that the same change closes.

## Goals / Non-Goals

**Goals:**

- Match the observed FortiOS `dns-entry` contract and keep fail-closed snapshots.
- Remove every identified path that deletes records the controller cannot recreate, or deletes records because its own view is stale or invalid.
- Keep one bad target, policy, or claim from stopping unrelated work.
- Make liveness, status, metrics, and alerts reflect what actually happened.
- Make unsafe chart configurations fail to render.

**Non-Goals:**

- Arbitrating one owner when several sources publish the same name, and ordering quota admission by existing provider state.
- Supporting the upstream `/target` annotation or zone-apex records.
- Scoping approval preconditions to the records a plan touches in shared zones.
- Waiting for in-flight work when an event-mode runtime is stopped by a target change.

## Decisions

### Convert names only at the provider boundary

Endpoints stay fully qualified everywhere in the controller. The client removes the zone suffix when writing `hostname` and appends it when reading. It writes `canonical-name` with a trailing dot, and reads an undotted value as zone-relative, which is how the device serves it. Rows written by earlier releases therefore read as `name.zone.zone`, and the planner replaces them rather than mistaking them for the intended records. Apex writes are rejected until the device's apex spelling is verified. The zone name is also used as the DNS domain, so the dns-database entry name must equal its `domain`.

### Paginate on `size`, not `next_idx`

On the device, `size` is the table total and `matched_count` is the page's row count. `next_idx` is the next start on non-final pages but the last index on the final page, and `limit_reached` is absent. The client advances by the rows returned and stops at `size`. It keeps every fail-closed check: stable `size`, consistent `matched_count`, non-empty page before completion, no overflow, unique numeric IDs, and a stable revision across pages. Verified live: pages of 1000, 10, 7, and 1 rows returned the same records and content revision.

### Treat uncertainty as incomplete, not absent

Stale HTTPRoute status marks Gateway discovery incomplete. A policy that fails to compile denies its namespace's candidates. Because those denials would otherwise make that namespace's records stale, cleanup is suppressed while any policy is invalid, and the plan records the policy precondition as incomplete. Exclusive ownership never covers non-A/AAAA/CNAME rows.

### Quiet cycles do not touch approval

A cycle with no actionable operation persists no plan and requires no approval. It still passes conflict operations to the provider: providers never write them, but they count them, and shared ownership can use them to converge an interrupted rebind. It marks earlier non-terminal plans Stale, so an approval cannot be reused after the state it approved has gone away and later returns with the same hash.

### Pair HTTPRoute names with the parents that serve them

Intersection is computed per parent attachment. Each resulting name receives only that parent's Gateway addresses, and hostname-over-IP preference is applied per name. The route's endpoints are staged against a local budget so an oversized route is rejected whole without consuming the budget of its siblings.

### Liveness counts audits, and ticks only when idle

In event mode every target audit counts as a heartbeat attempt. A resync tick counts only when there are no targets or the target list fails, so a wedged worker still fails liveness. The automatic window uses the larger of the interval and resync period.

### Release the Lease after work drains

Leader election runs on its own context. Shutdown cancels the work context, waits for the reconcile loop to return, and only then cancels the elector so `ReleaseOnCancel` releases the Lease. Losing the Lease outside shutdown returns an explicit error and a non-zero exit.

## Risks / Trade-offs

- [Upgrade replaces legacy rows] → Documented dry-run review; `cleanupPolicy=keep` leaves old rows for manual removal.
- [Invalid policy blocks all cleanup for a target] → Intentional fail-closed choice; each invalid policy is logged with its namespace and name.
- [Stale HTTPRoute status can freeze Gateway cleanup while a Gateway controller is down] → Preferred over deleting live records; creates and updates continue.
- [Hostnames rejected by DNS-1123, such as names with underscores, are no longer published] → Invalid names are reported with warnings. In exclusive mode their existing rows can become cleanup candidates, which the release notes call out.
- [Queue depth metric] → `QueueState.SetDepth` is not wired from the event workqueue yet, so the queue-depth gauge reports zero in event mode. Wiring it requires exposing the queue length from the platform runtime.
- [Status conditions without a typed approval error] → When approval is required and an error occurs with pending operations, PlanApproved reports PendingApproval; a typed approval error would let status distinguish it from an apply failure.
