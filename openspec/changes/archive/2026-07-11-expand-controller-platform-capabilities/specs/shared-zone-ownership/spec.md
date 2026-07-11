## ADDED Requirements

### Requirement: Confirmed claim authorizes existing-record mutation
In shared-zone mode, the controller SHALL update, replace, deactivate, or delete an existing FortiGate record only when a Confirmed ownership claim matches the target, normalized logical record key, provider ID, live fingerprint, controller identity, and resourceVersion captured by the current plan.

#### Scenario: Matching confirmed claim permits mutation
- **WHEN** a live record and Confirmed claim match every planned ownership field
- **THEN** the record may be mutated subject to plan, policy, and cleanup guards

#### Scenario: Missing claim blocks mutation
- **WHEN** a desired name collides with an existing provider record that has no matching Confirmed claim
- **THEN** the controller reports an ownership conflict and sends no mutating request for that logical record

### Requirement: Two-phase create ownership
The controller SHALL reserve an ownership claim before creating a provider record and SHALL mark it Confirmed only after an exact created record is observed with a stable provider ID. A Reserved claim SHALL NOT authorize destructive cleanup.

#### Scenario: Lost create response converges safely
- **WHEN** the provider commits a create but the response is lost
- **THEN** the next full audit confirms the Reserved claim only if one exact live record exists and does not create a duplicate

### Requirement: Explicit exact-match adoption
Adoption of a pre-existing provider record SHALL require an explicit adoption request, an unclaimed logical key, and an exact fingerprint match against the current stable provider snapshot.

#### Scenario: Exact unclaimed record is adopted
- **WHEN** an operator approves adoption and the requested fingerprint exactly matches one unclaimed live record
- **THEN** the controller creates a Confirmed claim without changing the provider record

#### Scenario: Changed adoption candidate is refused
- **WHEN** the live fingerprint changes after an adoption plan is produced
- **THEN** adoption is rejected and a new plan is required

### Requirement: Claim conflicts and orphaning fail closed
Claim resourceVersion conflicts, duplicate claims, missing live records, duplicate provider IDs, or fingerprint divergence SHALL transition the affected claim or status to Conflict or Orphaned and SHALL suppress destructive action for that logical record.

#### Scenario: Ownership object is deleted unexpectedly
- **WHEN** a Confirmed claim disappears while its provider record remains
- **THEN** shared-zone reconciliation treats the record as unowned and does not delete or adopt it automatically

### Requirement: Exclusive mode remains compatible
The existing explicit exclusive-zone mode SHALL continue to reconcile without ownership CRDs, and enabling shared-zone mode SHALL be explicit and mutually exclusive with exclusive ownership for a target.

#### Scenario: Existing installation upgrades unchanged
- **WHEN** an existing single-target installation upgrades without enabling shared-zone mode
- **THEN** its ownership validation and reconciliation behavior remain unchanged
