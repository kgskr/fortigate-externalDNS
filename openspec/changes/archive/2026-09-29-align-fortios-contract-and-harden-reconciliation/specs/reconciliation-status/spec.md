## MODIFIED Requirements

### Requirement: Per-target current status
The controller SHALL maintain one status object per target with Ready, DiscoveryComplete, ProviderReachable, OwnershipHealthy, PolicyAccepted, PlanApproved, and DriftFree conditions plus observed generations, provider revision, desired/current/conflict counts, last plan hash, and last audit/apply timestamps. OwnershipHealthy SHALL reflect planning conflicts and ownership errors, PlanApproved SHALL reflect the approval requirement and plan state, Ready SHALL carry a fixed reason naming where reconciliation stopped, and a failed status write MUST be logged without failing reconciliation.

#### Scenario: Target becomes healthy
- **WHEN** discovery, policy, ownership, provider snapshot, planning, approval when required, and apply all succeed
- **THEN** the target status reports Ready and DriftFree with current observed values

#### Scenario: One target fails independently
- **WHEN** one target is unreachable while another succeeds
- **THEN** only the failed target reports ProviderReachable false and the healthy target remains Ready

#### Scenario: Planning conflicts are reported
- **WHEN** a target's plan contains conflicts
- **THEN** OwnershipHealthy is false with an ownership-conflict reason even if the cycle otherwise succeeds

#### Scenario: Status cannot be written
- **WHEN** writing the status object fails, for example because RBAC denies it
- **THEN** the failure is logged with a fixed reason and the reconciliation result is unchanged

### Requirement: Status and history are bounded and sanitized
Status SHALL contain only bounded summaries and fixed-enumeration reasons. It MUST NOT contain API tokens, Secret data, authorization headers, raw provider bodies, full record dumps, or user-controlled values as metric label names. Consecutive audits with the same plan hash, phase, and counts SHALL NOT add history entries.

#### Scenario: Provider returns a sensitive error body
- **WHEN** a provider error body contains token-like or arbitrary content
- **THEN** status records a fixed provider-error reason and sanitized message without the body

#### Scenario: Unchanged audits repeat
- **WHEN** successive audits produce the same plan hash, phase, and counts
- **THEN** history keeps one entry for them while the last audit time still advances

### Requirement: Expanded bounded metrics
Prometheus metrics SHALL expose desired/current/drift/conflict counts, incomplete discovery, provider snapshot age, queue depth/retries, plans by phase, applies by outcome, and per-target readiness using bounded labels. Reconcile outcome and last-successful-reconcile metrics SHALL also be updated for target-mode audits.

#### Scenario: Metrics cardinality remains bounded
- **WHEN** arbitrary hostnames and Kubernetes object names are reconciled
- **THEN** no metric labels contain hostnames, source object UIDs, provider record IDs, or error strings

#### Scenario: Target-mode reconcile is recorded
- **WHEN** a target-mode audit completes, successfully or not
- **THEN** the reconcile counters are updated and a success advances the last-successful-reconcile timestamp

### Requirement: Optional operator assets
The Helm chart SHALL optionally render a dashboard ConfigMap, PrometheusRule-compatible alert examples, and a ServiceMonitor without requiring Prometheus Operator CRDs for a default installation. Staleness alerts SHALL aggregate across the release's replicas, and an absence alert SHALL fire when the controller's metrics disappear.

#### Scenario: Monitoring assets are disabled
- **WHEN** their Helm values are false
- **THEN** no monitoring-specific custom resources are rendered

#### Scenario: Standby replicas do not trigger staleness
- **WHEN** a leader reconciles successfully while standby replicas export a zero last-success timestamp
- **THEN** the staleness alert does not fire because it uses the most recent success across the release
