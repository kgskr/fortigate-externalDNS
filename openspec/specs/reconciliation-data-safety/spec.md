# reconciliation-data-safety Specification

## Purpose
Ensures reconciliation never corrupts DNS state on the FortiGate: records not owned
by this controller are never mutated, target changes are applied without leaving
duplicates, creates are not retried in a way that duplicates entries, stale cleanup
is idempotent and conflict-aware, provider IDs are required for mutations, FortiGate
error envelopes on HTTP 2xx are treated as failures, and configuration parses strictly
(failing closed).

## Requirements
### Requirement: Safe target replacement planning

The planner SHALL handle target changes for the same managed zone, DNS name, and record type without producing an unsafe unordered create-plus-delete sequence.

#### Scenario: Single target changes
- **WHEN** an owned FortiGate record for `app.example.com A` currently targets `1.1.1.1` and desired state targets `2.2.2.2`
- **THEN** the planner emits a replacement-safe operation or ordered operations that cannot leave duplicate owned records if the later step fails

#### Scenario: Multiple targets remain distinct
- **WHEN** desired state contains two A targets for the same DNS name
- **THEN** both FortiGate entries are represented without overwriting each other in the planner

### Requirement: Partial apply continues independent operations

The FortiGate apply layer SHALL continue applying independent operations after an individual operation fails and SHALL return an aggregate error for all failed operations.

#### Scenario: One bad record in batch
- **WHEN** one FortiGate operation fails and later operations in the same batch target different records
- **THEN** the controller attempts the later operations and returns an aggregated error containing the failed operation

#### Scenario: Apply summary logged
- **WHEN** a reconciliation batch completes with mixed success and failure
- **THEN** logs or metrics include attempted, succeeded, failed, skipped, and conflict counts without secret values

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

### Requirement: Owner ID delimiter safety

Owner IDs that would corrupt the managed-record comment round trip MUST be rejected at configuration validation. The managed comment uses `;` as the field delimiter and `=` as the key/value delimiter.

#### Scenario: Owner ID contains a delimiter
- **WHEN** the configured owner ID contains a `;` or `=` character
- **THEN** configuration validation fails with a clear error instead of writing a comment that parses back to a different owner ID

#### Scenario: Owner ID with spaces round-trips
- **WHEN** an owner ID containing spaces (but no `;` or `=`) is written to and read back from a managed record comment
- **THEN** the parsed owner ID equals the configured owner ID

### Requirement: Source-aware record equality

Record equality used to decide updates SHALL account for the owning source identity that is persisted in the managed comment, so a change of owning Kubernetes resource is reconciled rather than leaving stale source metadata that can mis-gate cleanup.

#### Scenario: Owning resource changes but target is unchanged
- **WHEN** an owned record's hostname, type, and targets are unchanged but its owning source (kind, namespace, or name) has changed
- **THEN** the planner emits an update that rewrites the managed comment to the current source identity

### Requirement: Logical-record conflicts block partial cleanup

The planner SHALL treat an unowned record with the same zone, DNS name, and type as
authoritative for the logical record, even when the current target differs from the
desired target. It MUST NOT delete or deactivate stale owned rows for that logical
record while an unowned logical sibling conflict exists.

#### Scenario: Unowned logical sibling has a different target
- **WHEN** desired state wants `app.example.com A -> 2.2.2.2` and FortiGate already has an unowned `app.example.com A -> 1.1.1.1`
- **THEN** the planner emits a conflict instead of creating the desired row or mutating the unowned row

#### Scenario: Stale owned row shares a conflicted logical record
- **WHEN** a stale owned row exists for the same zone, DNS name, and type as an unowned logical sibling conflict
- **THEN** cleanup for that owned row is suppressed until the logical conflict is resolved, preventing partial mutation of a contested DNS name
