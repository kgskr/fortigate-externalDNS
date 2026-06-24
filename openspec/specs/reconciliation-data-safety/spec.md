# reconciliation-data-safety Specification

## Purpose
TBD - created by archiving change harden-dns-reconciliation-safety. Update Purpose after archive.
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

