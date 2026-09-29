## ADDED Requirements

### Requirement: FortiOS dns-entry name encoding
The FortiGate client SHALL write `dns-entry` `hostname` values relative to the dns-database domain and CNAME `canonical-name` values as absolute names ending with a dot, SHALL map listed rows back to the fully qualified names FortiOS serves under the same rules, and MUST reject zone-apex names, names outside the zone, and record types other than A, AAAA, and CNAME without issuing a provider request.

#### Scenario: Zone-relative hostname is written
- **WHEN** the controller creates or updates `web.example.com` in the zone `example.com`
- **THEN** the request body carries `hostname=web` and the device serves `web.example.com`

#### Scenario: CNAME target is written as an absolute name
- **WHEN** the controller writes a CNAME whose target is `lb.example.net`
- **THEN** the request body carries `canonical-name=lb.example.net.` so FortiOS does not append the zone

#### Scenario: Legacy fully qualified row is listed
- **WHEN** a listed row stores `hostname=web.example.com` or an undotted `canonical-name=lb.example.net` in the zone `example.com`
- **THEN** the client reports the served names `web.example.com.example.com` and `lb.example.net.example.com` so planning replaces the row instead of treating it as the intended record

#### Scenario: Unwritable name or type
- **WHEN** an operation targets the zone apex, a name outside the zone, or a record type other than A, AAAA, or CNAME
- **THEN** the operation fails with an explicit error and no request is sent to FortiGate

## MODIFIED Requirements

### Requirement: Exclusive-zone ownership acknowledgement

The controller MUST NOT enable FortiGate mutations unless the operator explicitly acknowledges that the configured DNS database is exclusive to this controller, and it MUST NOT send or depend on an undocumented per-record comment field for ownership. Exclusive ownership SHALL cover only A, AAAA, and CNAME rows; rows of any other type MUST never be adopted, updated, deactivated, or deleted, while remaining visible to CNAME conflict detection.

#### Scenario: Write mode without acknowledgement
- **WHEN** dry-run is disabled without `fortigate-exclusive-zone-ownership`
- **THEN** configuration validation fails before any FortiGate mutation

#### Scenario: Exclusive zone listed
- **WHEN** exclusive-zone ownership is acknowledged and records are listed
- **THEN** every returned A, AAAA, and CNAME record is treated as controller-owned without reading or writing a `comment` property

#### Scenario: Non-address record types are preserved
- **WHEN** the exclusive database also contains NS, MX, TXT, PTR, or SRV rows and cleanup is destructive
- **THEN** those rows are never planned for update, deactivation, or deletion, and a desired CNAME at the same name is reported as a conflict

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

The FortiGate client SHALL request fixed-size pages, advance each request by the number of rows returned, and stop only when the collected rows equal the response `size`. It MUST fail the cycle when `size` is missing, negative, or changes between pages, when a present `matched_count` differs from the rows in that page, when a page returns no rows before `size` is reached, when more rows than `size` are collected, when provider IDs repeat, or when any multi-page revision is empty or changes. The inconsistent `next_idx` and optional `limit_reached` fields SHALL NOT drive pagination.

#### Scenario: Multiple response pages
- **WHEN** FortiGate reports a `size` larger than the rows returned in the first page
- **THEN** the client requests the next page at `start` plus the rows already returned and returns records from all pages, regardless of `next_idx` or `limit_reached`

#### Scenario: Pagination snapshot changes
- **WHEN** `size` changes, a page returns no rows before `size` is reached, provider IDs repeat, or successive pages report different revisions
- **THEN** the client returns an incomplete-snapshot error and no plan is applied

#### Scenario: Page metadata is inconsistent
- **WHEN** a page's `matched_count` differs from its returned rows or the collected rows exceed `size`
- **THEN** the client rejects the snapshot instead of planning from it

#### Scenario: Paginated revision is empty
- **WHEN** any page in a multi-page response has an empty revision
- **THEN** the client rejects the snapshot because stability cannot be proven

#### Scenario: Numeric origin key
- **WHEN** an integer-mkey FortiGate response encodes `q_origin_key` as a JSON number
- **THEN** the client accepts it as the provider ID instead of failing JSON decoding

### Requirement: Provider ID required for mutating existing records

The FortiGate client MUST NOT use DNS name as a fallback provider record ID for PUT or DELETE requests, MUST accept only plain non-negative integer provider IDs both when listing and before building a request path, and SHALL escape the zone path segment exactly once.

#### Scenario: Missing provider ID on update
- **WHEN** an update operation requires a FortiGate record identifier and the current record has no provider ID
- **THEN** the client skips or fails that operation with an explicit error and does not call a hostname-based endpoint

#### Scenario: Missing provider ID on delete
- **WHEN** a delete operation requires a FortiGate record identifier and the current record has no provider ID
- **THEN** the client skips or fails that operation with an explicit error and does not call a hostname-based endpoint

#### Scenario: Non-numeric provider ID
- **WHEN** a listed row or a planned operation carries a provider ID such as `..`, `1/2`, or `abc`
- **THEN** the list fails or the operation fails with an explicit error, and no request can address a different CMDB path

### Requirement: FortiGate response envelope validation

The FortiGate client SHALL treat a FortiGate error envelope as a failed request even when HTTP status is 2xx, SHALL bound every response body read, and MUST fail without retrying when a response exceeds that bound.

#### Scenario: Error envelope with HTTP 200
- **WHEN** FortiGate returns HTTP 200 with a body indicating `status=error` or an unsuccessful `http_status`
- **THEN** the client returns an error and does not interpret the response as a successful empty result

#### Scenario: Oversized response body
- **WHEN** a response body exceeds the client's read limit
- **THEN** the request fails with an explicit size error and is not retried

### Requirement: Logical-record conflicts block partial cleanup

The planner SHALL treat any unowned record with the same zone, DNS name, and type as authoritative for the logical record even when an owned exact-target row also exists, and it MUST NOT update, delete, deactivate, or create other rows for that logical record while the conflict exists. A desired DNS name with more than one CNAME target SHALL likewise be a conflict.

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

#### Scenario: Multiple CNAME targets for one name
- **WHEN** desired state contains two CNAME endpoints with different targets, or one CNAME endpoint with several targets, for the same DNS name
- **THEN** the planner emits one conflict and performs no mutation for that name
