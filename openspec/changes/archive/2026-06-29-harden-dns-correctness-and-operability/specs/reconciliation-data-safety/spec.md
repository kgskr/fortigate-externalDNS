## ADDED Requirements

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
