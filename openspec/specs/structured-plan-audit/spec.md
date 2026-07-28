# structured-plan-audit Specification

## Purpose
Defines canonical plans, exact-hash approval, apply revalidation, and bounded audit history.

## Requirements
### Requirement: Canonical reconciliation plan
The controller SHALL represent every provider mutation as a versioned canonical JSON plan whose identifier is the lowercase SHA-256 digest of its canonical bytes. The plan SHALL include target identity, provider snapshot revision, discovery generation, policy generation, referenced ownership resource versions, sorted operations, prerequisite edges, and safety decisions, and SHALL exclude timestamps, secrets, provider response bodies, and unstable map ordering.

#### Scenario: Equivalent inputs produce the same plan
- **WHEN** two reconciliations receive logically equivalent desired endpoints, provider records, policies, and ownership claims in different input orders
- **THEN** they produce byte-identical canonical JSON and the same plan identifier

#### Scenario: Sensitive values are absent
- **WHEN** a plan is serialized for logs, a file, or a Kubernetes object
- **THEN** API tokens, Secret contents, authorization headers, CA private material, and raw provider response bodies are absent

### Requirement: Optional exact-hash approval
The controller SHALL support a disabled-by-default approval mode that prevents every provider mutation until the exact current plan hash is approved. Long-running mode SHALL accept approval only from the designated plan object's approval-hash annotation, and one-shot mode SHALL accept approval only from an explicitly supplied hash.

#### Scenario: Matching approval permits apply
- **WHEN** approval mode is enabled and the supplied approval hash exactly matches the current plan identifier
- **THEN** the controller may apply the plan after all other safety checks pass

#### Scenario: Missing or different approval blocks apply
- **WHEN** approval mode is enabled and approval is missing or differs by any byte
- **THEN** the controller performs no provider mutation and reports a PendingApproval result

### Requirement: Stale plans fail closed
The apply layer MUST reject a plan if its target, provider revision, discovery generation, policy generation, ownership resourceVersion, or canonical hash no longer matches current state.

#### Scenario: Provider changes after approval
- **WHEN** the provider revision changes after a plan is approved but before apply begins
- **THEN** the controller rejects the plan, relists current state, and requires a newly generated approval

#### Scenario: Source or policy changes after approval
- **WHEN** a source object is removed or its matching publication policy changes after exact-hash approval but before apply begins
- **THEN** the controller rebuilds the complete plan, performs no provider mutation, marks a persisted plan stale, and requires a newly generated approval

### Requirement: Durable bounded audit outcome
Each persisted plan SHALL expose a terminal or current phase and per-operation outcome summaries, and the controller SHALL retain only the configured bounded number or age of completed plan objects without deleting pending plans.

#### Scenario: Partial independent progress is recorded
- **WHEN** one operation fails while an independent operation succeeds
- **THEN** the audit status records both outcomes and the plan phase reflects partial failure
