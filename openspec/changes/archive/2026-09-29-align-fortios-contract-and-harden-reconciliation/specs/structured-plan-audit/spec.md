## MODIFIED Requirements

### Requirement: Canonical reconciliation plan
The controller SHALL represent every provider mutation as a versioned canonical JSON plan whose identifier is the lowercase SHA-256 digest of its canonical bytes. The plan SHALL include target identity, provider snapshot revision, discovery generation, policy generation and completeness, referenced ownership resource versions, sorted operations, prerequisite edges, and safety decisions, and SHALL exclude timestamps, secrets, provider response bodies, and unstable map ordering.

#### Scenario: Equivalent inputs produce the same plan
- **WHEN** two reconciliations receive logically equivalent desired endpoints, provider records, policies, and ownership claims in different input orders
- **THEN** they produce byte-identical canonical JSON and the same plan identifier

#### Scenario: Sensitive values are absent
- **WHEN** a plan is serialized for logs, a file, or a Kubernetes object
- **THEN** API tokens, Secret contents, authorization headers, CA private material, and raw provider response bodies are absent

#### Scenario: Invalid policy is recorded
- **WHEN** a plan is built while a policy failed to compile
- **THEN** its policy precondition is recorded as incomplete

### Requirement: Optional exact-hash approval
The controller SHALL support a disabled-by-default approval mode that prevents every provider mutation until the exact current plan hash is approved. Long-running mode SHALL accept approval only from the designated plan object's approval-hash annotation, and one-shot mode SHALL accept approval only from an explicitly supplied hash. A reconciliation with no actionable operation, meaning none or only conflicts, MUST NOT persist a plan or require approval, and SHALL mark older non-terminal plans for the target Stale so their approval cannot be reused.

#### Scenario: Matching approval permits apply
- **WHEN** approval mode is enabled and the supplied approval hash exactly matches the current plan identifier
- **THEN** the controller may apply the plan after all other safety checks pass

#### Scenario: Missing or different approval blocks apply
- **WHEN** approval mode is enabled and approval is missing or differs by any byte
- **THEN** the controller performs no provider mutation and reports a PendingApproval result

#### Scenario: Quiet cycle after a pending or approved plan
- **WHEN** a plan is pending or approved and a later cycle has no actionable operation because its source was deleted, the provider already converged, or only conflicts remain
- **THEN** no new plan is created, the earlier plan becomes Stale, no mutation is sent, and the cycle reports success

### Requirement: Durable bounded audit outcome
Each persisted plan SHALL expose a terminal or current phase and per-operation outcome summaries, and the controller SHALL retain only the configured bounded number or age of completed plan objects without deleting pending plans. A terminal phase MUST still be recorded when the reconcile deadline expires mid-apply, using a bounded write detached from the expired deadline, but SHALL NOT be written after shutdown or leadership loss.

#### Scenario: Partial independent progress is recorded
- **WHEN** one operation fails while an independent operation succeeds
- **THEN** the audit status records both outcomes and the plan phase reflects partial failure

#### Scenario: Reconcile deadline expires mid-apply
- **WHEN** the reconcile timeout fires while an approved plan is applying and the controller is still leading
- **THEN** the plan records the Interrupted phase instead of remaining Applying
