# reconciliation-data-safety Specification

## Purpose
Ensures exclusive-zone reconciliation never plans from incomplete state, deletes the last known-good target after a failed dependency, duplicates records through retries, mutates without provider IDs, accepts FortiGate error envelopes, or performs unbounded cleanup.

## Requirements
### Requirement: Safe target replacement planning

The planner SHALL handle target and record-type changes for the same managed zone and DNS name without removing the last known-good target before every compatible desired target is observable.

#### Scenario: Single target changes
- **WHEN** an owned FortiGate record for `app.example.com A` currently targets `1.1.1.1` and desired state targets `2.2.2.2`
- **THEN** the planner emits an in-place replacement operation using the existing provider ID

#### Scenario: Multiple targets remain distinct
- **WHEN** desired state contains two A targets for the same DNS name
- **THEN** both FortiGate entries are represented without overwriting each other in the planner

#### Scenario: Missing desired targets defer cleanup
- **WHEN** one or more desired targets are missing while stale targets still exist for the logical record
- **THEN** the planner creates missing targets but withholds stale cleanup until a later snapshot contains every desired target

#### Scenario: One-to-one record type transition
- **WHEN** one owned A, AAAA, or CNAME row must change to the incompatible CNAME or address type and has a provider ID
- **THEN** the planner emits one keyed in-place replacement instead of an invalid create-before-cleanup sequence

#### Scenario: Multi-row CNAME transition
- **WHEN** a CNAME-to-address or address-to-CNAME transition has more than one current or desired row
- **THEN** the planner emits a conflict and performs no mutation for that DNS name

### Requirement: Partial apply continues independent operations

The FortiGate apply layer SHALL continue applying independent operations after an individual operation fails, MUST skip cleanup for a DNS name whose prerequisite create, update, or replace failed, and SHALL return an aggregate error for all failed operations.

#### Scenario: One bad record in batch
- **WHEN** one FortiGate operation fails and later operations in the same batch target different records
- **THEN** the controller attempts the later operations and returns an aggregated error containing the failed operation

#### Scenario: Prerequisite mutation fails
- **WHEN** a create, update, or replace fails and a later delete or deactivate targets the same zone and DNS name
- **THEN** dependent cleanup is skipped while independent DNS names can continue

#### Scenario: Apply summary logged
- **WHEN** a reconciliation batch completes with mixed success and failure
- **THEN** logs or metrics include attempted, succeeded, failed, skipped, and conflict counts without secret values

### Requirement: Exclusive-zone ownership acknowledgement

The controller MUST NOT enable FortiGate mutations unless the operator explicitly acknowledges that the configured DNS database is exclusive to this controller, and it MUST NOT send or depend on an undocumented per-record comment field for ownership.

#### Scenario: Write mode without acknowledgement
- **WHEN** dry-run is disabled without `fortigate-exclusive-zone-ownership`
- **THEN** configuration validation fails before any FortiGate mutation

#### Scenario: Exclusive zone listed
- **WHEN** exclusive-zone ownership is acknowledged and records are listed
- **THEN** every returned record is treated as controller-owned without reading or writing a `comment` property

#### Scenario: Restricted destructive cleanup
- **WHEN** exclusive-zone mode uses source or namespace restrictions with a destructive cleanup policy
- **THEN** configuration validation requires `cleanup-policy=keep` or unrestricted exclusive-zone scope

#### Scenario: Restricted existing record differs
- **WHEN** exclusive-zone mode uses restricted source or namespace discovery with `cleanup-policy=keep` and a current row differs from desired target, type, TTL, or status
- **THEN** the row is not adopted as mutable ownership and reconciliation fails closed with a conflict instead of updating or replacing it

#### Scenario: Restricted exact match or missing name
- **WHEN** restricted exclusive-zone discovery finds an exact current desired record or a genuinely missing DNS name
- **THEN** the exact record is accepted without mutation and the missing name can be created

### Requirement: Complete FortiGate collection snapshot

The FortiGate client SHALL follow paginated list responses until every matched record is collected and MUST fail the cycle when metadata is missing, pagination does not advance, provider IDs repeat, any multi-page revision is empty or changes, or the terminal result count is incomplete.

#### Scenario: Multiple response pages
- **WHEN** FortiGate returns `limit_reached=true` with the last returned `next_idx`
- **THEN** the client requests `start=next_idx+1` and returns records from all pages

#### Scenario: Pagination snapshot changes
- **WHEN** a cursor fails to advance, provider IDs repeat, or successive pages report different revisions
- **THEN** the client returns an incomplete-snapshot error and no plan is applied

#### Scenario: Paginated revision is empty
- **WHEN** any page in a multi-page response has an empty revision
- **THEN** the client rejects the snapshot because stability cannot be proven

#### Scenario: Numeric origin key
- **WHEN** an integer-mkey FortiGate response encodes `q_origin_key` as a JSON number
- **THEN** the client accepts it as the provider ID instead of failing JSON decoding

### Requirement: Provider ID required for mutating existing records

The FortiGate client MUST NOT use DNS name as a fallback provider record ID for PUT or DELETE requests.

#### Scenario: Missing provider ID on update
- **WHEN** an update operation requires a FortiGate record identifier and the current record has no provider ID
- **THEN** the client skips or fails that operation with an explicit error and does not call a hostname-based endpoint

#### Scenario: Missing provider ID on delete
- **WHEN** a delete operation requires a FortiGate record identifier and the current record has no provider ID
- **THEN** the client skips or fails that operation with an explicit error and does not call a hostname-based endpoint

### Requirement: FortiGate response envelope validation

The FortiGate client SHALL treat a FortiGate error envelope as a failed request even when HTTP status is 2xx.

#### Scenario: Error envelope with HTTP 200
- **WHEN** FortiGate returns HTTP 200 with a body indicating `status=error` or an unsuccessful `http_status`
- **THEN** the client returns an error and does not interpret the response as a successful empty result

### Requirement: Strict typed environment parsing

Configuration loading MUST fail when a non-empty typed environment variable cannot be parsed.

#### Scenario: Malformed dry-run variable
- **WHEN** `DRY_RUN=ture` is present
- **THEN** configuration loading fails with a clear error instead of defaulting to write-enabled mode

#### Scenario: Malformed duration variable
- **WHEN** a duration environment variable contains an invalid value
- **THEN** configuration loading fails with a clear error naming the variable

### Requirement: Non-mutating normalization

DNS endpoint normalization SHALL NOT mutate caller-owned target slices.

#### Scenario: Caller reuses targets slice
- **WHEN** an endpoint is normalized
- **THEN** the original target slice supplied by the caller remains unchanged

### Requirement: Retry-safe record creation

Automatic retry of a FortiGate `dns-entry` create MUST NOT be able to produce a duplicate owned record. Because a `dns-entry` mkey is a server-assigned integer with no client-supplied idempotency key, the client SHALL NOT blindly re-issue the create `POST` after a retryable failure (HTTP 429, HTTP 5xx, or transport error).

#### Scenario: Create POST fails after the server committed the record
- **WHEN** a create request fails with a retryable error after FortiGate has already committed the new entry
- **THEN** the controller does not silently re-`POST` the identical body in a way that creates a second entry for the same hostname and target

#### Scenario: Idempotent methods still retry
- **WHEN** an update, replace, deactivate, or delete request (which targets a specific provider ID) fails with a retryable error
- **THEN** the client still retries it, because re-issuing a keyed request is safe

### Requirement: Idempotent stale cleanup

Stale-record cleanup SHALL NOT re-issue a mutation when the current record already matches the cleanup-desired state.

#### Scenario: Already-disabled record under deactivate policy
- **WHEN** the cleanup policy is `deactivate` and a stale owned record is already disabled on the device
- **THEN** the planner emits no deactivate operation for that record, so no redundant `PUT` is sent on subsequent reconcile loops

### Requirement: Duplicate owned-row reconciliation

The planner SHALL NOT silently discard a current owned record that shares an identity key with another current owned record. Duplicate owned rows for the same zone, name, type, and target SHALL be observable and reducible to a single record.

#### Scenario: Two owned rows share the same key
- **WHEN** the current FortiGate state contains two owned `dns-entry` rows that normalize to the same record key
- **THEN** the planner either emits an operation to remove the extra owned duplicate(s) under the configured cleanup policy or surfaces the duplicate in a warning, rather than overwriting one in memory and leaving it unmanaged

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

### Requirement: Mass-cleanup guard

The controller SHALL refuse cleanup when complete discovery produces an empty desired set without explicit override or exceeds the configured numeric cap; refused cleanup MUST NOT block creates or updates and MUST be logged and counted.

#### Scenario: Discovery returns successfully empty
- **WHEN** a misconfiguration (such as a wrong domain filter or namespace) causes discovery to succeed with zero desired endpoints while owned records exist on the device
- **THEN** no owned record is deleted or deactivated that cycle, an error log reports the refusal with the planned count, and the refusal metric increments

#### Scenario: Intentional decommissioning
- **WHEN** the operator runs with `--allow-empty-desired-cleanup` (for example with `--once` during teardown) and the desired set is empty
- **THEN** cleanup proceeds under the configured cleanup policy

#### Scenario: Numeric cap exceeded
- **WHEN** `--max-cleanup-per-cycle=10` is configured and a cycle plans 25 owned-record cleanups
- **THEN** none of the 25 cleanup operations is applied that cycle, while planned creates and updates still apply

#### Scenario: Guard does not persist across recovery
- **WHEN** a cycle's cleanup was refused and a later cycle's discovery produces a non-empty desired set within any configured cap
- **THEN** the later cycle plans and applies cleanup normally

#### Scenario: Partial discovery failure remains fail-closed
- **WHEN** any configured source is incomplete or discovery returns an error
- **THEN** no cleanup is planned from the incomplete state, regardless of guard configuration

### Requirement: Destructive operations require a fresh complete audit
Delete and deactivate operations SHALL require a single reconciliation that completed all configured Kubernetes source lists, EndpointSlice reads when needed, policy reads, ownership reads, and a stable complete provider snapshot. Event identity alone SHALL never authorize cleanup.

#### Scenario: Informer cache is not synchronized
- **WHEN** a worker starts before every required informer cache reports synchronized
- **THEN** it performs no provider mutation and reports discovery incomplete

#### Scenario: Non-destructive event run lacks cleanup evidence
- **WHEN** safe desired creation can be proven but one cleanup prerequisite is incomplete
- **THEN** create or exact update may proceed and every cleanup operation is suppressed

### Requirement: Apply revalidates plan identity and ownership
Immediately before each mutation, apply SHALL verify the plan hash, target generation, provider revision, policy generation, prerequisite outcomes, and every referenced ownership resourceVersion. A mismatch SHALL block the affected mutation and its dependent cleanup.

#### Scenario: Ownership changes mid-apply
- **WHEN** another controller updates a referenced claim after planning
- **THEN** the operation is rejected without a provider request and independent operations may continue

### Requirement: Existing cleanup guards remain cumulative
Empty-desired protection, maximum cleanup count, source-incomplete suppression, logical-record conflicts, and dependent-cleanup blocking SHALL remain active in exclusive and shared modes and SHALL be evaluated in addition to approval and ownership rules.

#### Scenario: Approved plan exceeds cleanup cap
- **WHEN** a correctly approved plan contains more cleanup operations than the configured maximum
- **THEN** cleanup is refused despite approval

### Requirement: Bearer-authenticated provider transport is HTTPS-only
Every direct or declarative FortiGate target that can receive an API bearer token MUST use an absolute `https://` URL, and the credential-bearing client MUST reject redirects rather than forwarding authentication to another origin or protocol.

#### Scenario: Cleartext target is configured
- **WHEN** direct configuration, a target custom resource, or Helm values specify an `http://` FortiGate URL
- **THEN** validation fails before an authenticated provider request is constructed

#### Scenario: FortiGate responds with a redirect
- **WHEN** an HTTPS FortiGate endpoint responds with a redirect to another URL
- **THEN** the client returns an error without issuing a request to the redirect destination

### Requirement: Cross-target operations are never atomic dependencies
A plan SHALL contain operations for exactly one target. Failure on one target SHALL NOT authorize rollback, cleanup, or mutation on another target.

#### Scenario: One target apply fails
- **WHEN** a target plan partially fails
- **THEN** another target's plan and ownership state remain unchanged
