# reconciliation-data-safety Delta

## ADDED Requirements

### Requirement: Mass-cleanup guard

The controller SHALL refuse to apply cleanup operations (deletes under the
`delete` policy, deactivations under the `deactivate` policy) for a reconcile
cycle in which discovery succeeded but produced an empty desired set while
current owned records exist, unless an explicit override
(`--allow-empty-desired-cleanup`) is configured. The controller SHALL also
support an opt-in numeric cap (`--max-cleanup-per-cycle`, default unlimited)
that refuses the cycle's cleanup when planned cleanup operations exceed it.
Refused cleanup MUST NOT block create or update operations in the same cycle,
MUST be logged at error level with the planned cleanup count, and MUST
increment a dedicated refusal metric.

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

- **WHEN** discovery returns an error
- **THEN** no cleanup is planned from the incomplete state, regardless of guard configuration
