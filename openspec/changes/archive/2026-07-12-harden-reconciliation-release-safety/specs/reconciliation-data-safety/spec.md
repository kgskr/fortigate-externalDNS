## ADDED Requirements

### Requirement: Exclusive-zone ownership acknowledgement
The controller MUST NOT enable FortiGate mutations unless the operator explicitly acknowledges that the configured DNS database is exclusive to this controller, and it MUST NOT send or depend on an undocumented per-record comment field for ownership.

#### Scenario: Write mode without acknowledgement
- **WHEN** dry-run is disabled without `fortigate-exclusive-zone-ownership`
- **THEN** configuration validation fails before any FortiGate mutation

#### Scenario: Exclusive zone listed
- **WHEN** exclusive-zone ownership is acknowledged and the controller lists records from the configured DNS database
- **THEN** all returned records are treated as controller-owned without reading or writing a `comment` property

#### Scenario: Restricted destructive cleanup
- **WHEN** exclusive-zone mode uses source or namespace restrictions with a destructive cleanup policy
- **THEN** configuration validation fails and directs the operator to use `cleanup-policy=keep` or an unrestricted exclusive-zone scope

#### Scenario: Restricted existing record differs
- **WHEN** exclusive-zone mode uses restricted source or namespace discovery with `cleanup-policy=keep` and a current row differs from desired target, type, TTL, or status
- **THEN** the row is not adopted as mutable ownership and reconciliation fails closed with a conflict instead of updating or replacing it

#### Scenario: Restricted exact match or missing name
- **WHEN** restricted exclusive-zone discovery finds an exact current desired record or a genuinely missing DNS name
- **THEN** the exact record is accepted without mutation and the missing name can be created

### Requirement: Complete FortiGate collection snapshot
The FortiGate client SHALL follow paginated list responses until every matched record is collected and MUST fail the reconcile cycle when pagination metadata is missing, pagination does not advance, provider IDs repeat across pages, the snapshot revision changes, or the terminal result count is incomplete.

#### Scenario: Multiple response pages
- **WHEN** FortiGate returns `limit_reached=true` with the last returned `next_idx`
- **THEN** the client requests `start=next_idx+1` and returns records from all pages

#### Scenario: Non-advancing cursor
- **WHEN** a limited response returns a `next_idx` that does not advance
- **THEN** the client returns an incomplete-snapshot error and no plan is applied

#### Scenario: Revision changes during pagination
- **WHEN** successive pages report different non-empty revisions
- **THEN** the client rejects the mixed snapshot and retries from fresh state on a later reconcile

#### Scenario: Paginated revision is empty
- **WHEN** any page in a multi-page response has an empty revision
- **THEN** the client rejects the snapshot because stability cannot be proven

#### Scenario: Numeric origin key
- **WHEN** an integer-mkey FortiGate response encodes `q_origin_key` as a JSON number
- **THEN** the client accepts it as the provider ID instead of failing JSON decoding

## MODIFIED Requirements

### Requirement: Safe target replacement planning
The planner SHALL handle target changes for the same managed zone, DNS name, and record type without producing cleanup that can remove the last known-good target before every desired target is observable.

#### Scenario: Single target changes
- **WHEN** an owned FortiGate record for `app.example.com A` currently targets `1.1.1.1` and desired state targets `2.2.2.2`
- **THEN** the planner emits an in-place replacement operation using the existing provider ID

#### Scenario: Multiple targets are introduced
- **WHEN** one or more desired targets for a logical record are missing while stale targets still exist
- **THEN** the planner emits creates but withholds cleanup for that logical record until a later snapshot contains every desired target

#### Scenario: Desired targets become observable
- **WHEN** a later snapshot contains every desired target and still contains stale targets
- **THEN** the planner emits cleanup for the stale targets under the configured cleanup policy

#### Scenario: One-to-one record type transition
- **WHEN** one owned A, AAAA, or CNAME row must change to the incompatible CNAME or address type and has a provider ID
- **THEN** the planner emits one keyed in-place replacement instead of an invalid create-before-cleanup sequence

#### Scenario: Multi-row CNAME transition
- **WHEN** a CNAME-to-address or address-to-CNAME transition has more than one current or desired row
- **THEN** the planner emits a conflict and performs no mutation for that DNS name

### Requirement: Partial apply continues independent operations
The FortiGate apply layer SHALL continue applying independent operations after an individual operation fails, MUST skip cleanup for a DNS name whose prerequisite create, update, or replace failed, and SHALL return an aggregate error for all failed operations.

#### Scenario: One bad record in batch
- **WHEN** one FortiGate operation fails and later operations in the same batch target different logical records
- **THEN** the controller attempts the later operations and returns an aggregated error containing the failed operation

#### Scenario: Prerequisite mutation fails
- **WHEN** a create, update, or replace fails and a later delete or deactivate targets the same zone and DNS name
- **THEN** the cleanup is skipped while cleanup for independent DNS names can continue

#### Scenario: Apply summary logged
- **WHEN** a reconciliation batch completes with mixed success, failure, and dependency skips
- **THEN** logs or metrics include attempted, succeeded, failed, skipped, and conflict counts without secret values

### Requirement: Logical-record conflicts block partial cleanup
The planner SHALL treat any unowned record with the same zone, DNS name, and type as authoritative for the logical record even when an owned exact-target row also exists, and it MUST NOT update, delete, deactivate, or create other rows for that logical record while the conflict exists.

#### Scenario: Owned match plus unowned sibling
- **WHEN** desired state exactly matches an owned row but an unowned row exists for the same logical record
- **THEN** the planner emits a conflict and no mutation for that logical record

#### Scenario: Unowned logical sibling has a different target
- **WHEN** desired state wants `app.example.com A -> 2.2.2.2` and FortiGate already has an unowned `app.example.com A -> 1.1.1.1`
- **THEN** the planner emits a conflict instead of creating the desired row or mutating the unowned row

#### Scenario: Stale owned row shares a conflicted logical record
- **WHEN** a stale owned row exists for the same zone, DNS name, and type as an unowned logical sibling conflict
- **THEN** cleanup for that owned row is suppressed until the logical conflict is resolved

#### Scenario: Desired CNAME and address records conflict
- **WHEN** desired state contains a CNAME and an A or AAAA record for the same DNS name
- **THEN** the planner emits one conflict and performs no create, update, replace, or cleanup for that name

## REMOVED Requirements

### Requirement: Owner ID delimiter safety
**Reason**: Owner IDs are no longer serialized into an undocumented FortiGate comment field.
**Migration**: Use explicit exclusive-zone ownership; owner ID remains an in-process identity and diagnostic value only.

### Requirement: Source-aware record equality
**Reason**: Source identity cannot be persisted in the documented FortiGate `dns-entry` schema.
**Migration**: Exclusive-zone cleanup is bounded by complete discovery and validated scope rather than per-record source comments.
