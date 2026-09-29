## MODIFIED Requirements

### Requirement: Declarative FortiGate targets
The controller SHALL support namespaced target objects that reference, but never copy, API-token Secret keys and optional CA bundle keys and that declare URL, VDOM, zone, ownership mode, discovery scope, cleanup policy, reconcile timing, and approval mode. Token values SHALL be trimmed of surrounding whitespace, `spec.timeout` SHALL bound each provider request while the global reconcile timeout bounds each reconciliation, `spec.retries` SHALL default to 2, and a target domain filter MUST equal the target zone or lie inside it.

#### Scenario: Target Secret is resolved
- **WHEN** a valid target references an authorized Secret key
- **THEN** the controller constructs the target client in memory and never persists the Secret value in status or plans

#### Scenario: Secret reference is missing
- **WHEN** the referenced Secret or key does not exist
- **THEN** that target reports a credential condition and performs no provider request

#### Scenario: Token Secret has a trailing newline
- **WHEN** the referenced token value ends with a newline, as produced by `kubectl create secret --from-file`
- **THEN** the trimmed token is used, and an all-whitespace value reports the token-empty reason

#### Scenario: Domain filter outside the zone
- **WHEN** a target with zone `example.com` declares the domain filter `other.org`
- **THEN** the target is invalid and writes nothing into the `example.com` database

### Requirement: Target failure isolation
Each target SHALL have independent client state, queue key, retries, circuit state, plan, ownership claims, and status so failure or backoff for one target does not block another. An invalid target SHALL be excluded with a fixed reason in its status and metrics while valid targets keep reconciling, credential and client-construction failures SHALL keep their specific reason and still write target status, and a transient failure to list targets MUST NOT terminate the long-running process.

#### Scenario: Concurrent healthy and failing targets
- **WHEN** one target repeatedly returns retryable errors and another receives a source event
- **THEN** the healthy target reconciles without waiting for the failed target's backoff

#### Scenario: One target is invalid
- **WHEN** one target fails validation while others are valid
- **THEN** only that target is excluded with a `target-invalid` reason, and in event mode only its own audits fail

#### Scenario: Target list fails transiently
- **WHEN** listing target objects fails in long-running polling or event mode
- **THEN** the failure is logged and counted, the cycle is retried later, and the process keeps running; `--once` still returns the error

#### Scenario: Client construction fails
- **WHEN** a target's credentials resolve but the provider client cannot be built, for example because its CA bundle contains no certificate
- **THEN** the target status records `Ready=False` with a fixed reason and the sanitized error is logged without the token

### Requirement: Overlapping write scopes are rejected
The controller SHALL reject simultaneously write-enabled targets whose normalized domain scopes overlap unless both are explicitly configured for non-destructive keep behavior and acknowledge overlap. Rejection MUST exclude both overlapping targets with a scope-conflict reason while non-overlapping targets continue.

#### Scenario: Parent and child suffix overlap
- **WHEN** write targets select `example.com` and `apps.example.com`
- **THEN** configuration is invalid for both targets, which are excluded with a scope-conflict reason unless the non-destructive overlap exception is satisfied, and other targets continue to reconcile
