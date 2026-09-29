# shared-zone-ownership Specification

## Purpose
Defines claim-based coordination for safe mutation and adoption in shared FortiGate DNS zones.
## Requirements
### Requirement: Confirmed claim authorizes existing-record mutation
In shared-zone mode, the controller SHALL update, replace, deactivate, or delete an existing FortiGate record only when a Confirmed ownership claim matches the target, normalized logical record key, provider ID, live fingerprint, controller identity, and resourceVersion captured by the current plan.

#### Scenario: Matching confirmed claim permits mutation
- **WHEN** a live record and Confirmed claim match every planned ownership field
- **THEN** the record may be mutated subject to plan, policy, and cleanup guards

#### Scenario: Missing claim blocks mutation
- **WHEN** a desired name collides with an existing provider record that has no matching Confirmed claim
- **THEN** the controller reports an ownership conflict and sends no mutating request for that logical record

### Requirement: Two-phase create ownership
The controller SHALL reserve an ownership claim before creating a provider record and SHALL mark it Confirmed only after an exact created record is observed with a stable provider ID. A Reserved claim SHALL NOT authorize destructive cleanup. A claim created without a phase because its reserve was interrupted SHALL be completed to Reserved when its spec matches the request.

#### Scenario: Lost create response converges safely
- **WHEN** the provider commits a create but the response is lost
- **THEN** the next full audit confirms the Reserved claim only if one exact live record exists and does not create a duplicate

#### Scenario: Reserve interrupted after claim creation
- **WHEN** a claim was created but its transition to Reserved was interrupted, and a later reserve carries the same spec
- **THEN** the claim is moved to Reserved instead of failing as a conflict on every cycle

### Requirement: Explicit exact-match adoption
Adoption of a pre-existing provider record SHALL require an explicit adoption request, an unclaimed logical key, and an exact fingerprint match against the current stable provider snapshot.

#### Scenario: Exact unclaimed record is adopted
- **WHEN** an operator approves adoption and the requested fingerprint exactly matches one unclaimed live record
- **THEN** the controller creates a Confirmed claim without changing the provider record

#### Scenario: Changed adoption candidate is refused
- **WHEN** the live fingerprint changes after an adoption plan is produced
- **THEN** adoption is rejected and a new plan is required

### Requirement: Claim conflicts and orphaning fail closed
Claim resourceVersion conflicts, duplicate claims, missing live records, duplicate provider IDs, or fingerprint divergence SHALL transition the affected claim or status to Conflict or Orphaned and SHALL suppress destructive action for that logical record. An Orphaned claim MUST return to Reserved only when a stable provider snapshot proves that no row of its record identity or bound provider ID remains.

#### Scenario: Ownership object is deleted unexpectedly
- **WHEN** a Confirmed claim disappears while its provider record remains
- **THEN** shared-zone reconciliation treats the record as unowned and does not delete or adopt it automatically

#### Scenario: Orphaned claim with a live row
- **WHEN** a reserve targets an Orphaned claim and the stable snapshot still contains a row of that identity or its bound provider ID
- **THEN** the claim is not recovered and the reserve is refused

### Requirement: Exclusive mode remains compatible
The existing explicit exclusive-zone mode SHALL continue to reconcile without ownership CRDs, and enabling shared-zone mode SHALL be explicit and mutually exclusive with exclusive ownership for a target.

#### Scenario: Existing installation upgrades unchanged
- **WHEN** an existing single-target installation upgrades without enabling shared-zone mode
- **THEN** its ownership validation and reconciliation behavior remain unchanged

### Requirement: Controller-initiated deletes release their claim
After a shared-mode delete succeeds and a stable provider relist proves that neither the bound provider ID nor any row of the claim's record identity remains, the controller SHALL release the claim finalizer and delete the claim with a resourceVersion precondition so the hostname can be published again.

#### Scenario: Deleted record releases its claim
- **WHEN** the controller deletes a record whose source was removed and the stable relist shows the row is gone
- **THEN** the claim's finalizer is released and the claim is deleted

#### Scenario: Deleted hostname is published again
- **WHEN** a source with the same hostname appears after its record was deleted
- **THEN** a claim is reserved, the record is created, and the claim is confirmed instead of being refused as orphaned

### Requirement: Interrupted claim writes converge
Two-step claim writes SHALL converge after interruption without weakening ownership. An interrupted rebind MUST only be completed when the claim is Confirmed for this controller, its bound provider ID, record identity, and source match, and the single live row with that ID exactly equals the desired record; every other case SHALL remain a conflict.

#### Scenario: Rebind interrupted after provider update
- **WHEN** a provider update succeeded but the claim rebind was not persisted, and the next cycle observes one live row that exactly equals the desired record under the claim's bound ID and source
- **THEN** the claim is rebound to the live fingerprint and later cycles see the row as owned

#### Scenario: Rebind evidence does not match
- **WHEN** the live row differs from the desired record, the provider ID is duplicated or different, or the claim's source does not match
- **THEN** no rebind occurs and the operation remains a conflict

