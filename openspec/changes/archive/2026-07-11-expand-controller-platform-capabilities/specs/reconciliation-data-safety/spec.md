## ADDED Requirements

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

### Requirement: Cross-target operations are never atomic dependencies
A plan SHALL contain operations for exactly one target. Failure on one target SHALL NOT authorize rollback, cleanup, or mutation on another target.

#### Scenario: One target apply fails
- **WHEN** a target plan partially fails
- **THEN** another target's plan and ownership state remain unchanged
