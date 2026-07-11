## ADDED Requirements

### Requirement: Declarative FortiGate targets
The controller SHALL support namespaced target objects that reference, but never copy, API-token Secret keys and optional CA bundle keys and that declare URL, VDOM, zone, ownership mode, discovery scope, cleanup policy, reconcile timing, and approval mode.

#### Scenario: Target Secret is resolved
- **WHEN** a valid target references an authorized Secret key
- **THEN** the controller constructs the target client in memory and never persists the Secret value in status or plans

#### Scenario: Secret reference is missing
- **WHEN** the referenced Secret or key does not exist
- **THEN** that target reports a credential condition and performs no provider request

### Requirement: Target failure isolation
Each target SHALL have independent client state, queue key, retries, circuit state, plan, ownership claims, and status so failure or backoff for one target does not block another.

#### Scenario: Concurrent healthy and failing targets
- **WHEN** one target repeatedly returns retryable errors and another receives a source event
- **THEN** the healthy target reconciles without waiting for the failed target's backoff

### Requirement: Overlapping write scopes are rejected
The controller SHALL reject simultaneously write-enabled targets whose normalized domain scopes overlap unless both are explicitly configured for non-destructive keep behavior and acknowledge overlap.

#### Scenario: Parent and child suffix overlap
- **WHEN** write targets select `example.com` and `apps.example.com`
- **THEN** configuration is invalid unless the non-destructive overlap exception is satisfied

### Requirement: Legacy single-target compatibility
Existing CLI and environment settings SHALL synthesize a default target when CRD-managed target mode is disabled. Direct credential flags and CRD-managed multi-target mode SHALL be mutually exclusive.

#### Scenario: Legacy deployment starts after upgrade
- **WHEN** existing flags are supplied and target mode is not enabled
- **THEN** one default target is reconciled with existing behavior
