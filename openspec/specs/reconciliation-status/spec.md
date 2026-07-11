# reconciliation-status Specification

## Purpose
Defines bounded, sanitized status, history, and metrics for each target.

## Requirements
### Requirement: Per-target current status
The controller SHALL maintain one status object per target with Ready, DiscoveryComplete, ProviderReachable, OwnershipHealthy, PolicyAccepted, PlanApproved, and DriftFree conditions plus observed generations, provider revision, desired/current/conflict counts, last plan hash, and last audit/apply timestamps.

#### Scenario: Target becomes healthy
- **WHEN** discovery, policy, ownership, provider snapshot, planning, approval when required, and apply all succeed
- **THEN** the target status reports Ready and DriftFree with current observed values

#### Scenario: One target fails independently
- **WHEN** one target is unreachable while another succeeds
- **THEN** only the failed target reports ProviderReachable false and the healthy target remains Ready

### Requirement: Status and history are bounded and sanitized
Status SHALL contain only bounded summaries and fixed-enumeration reasons. It MUST NOT contain API tokens, Secret data, authorization headers, raw provider bodies, full record dumps, or user-controlled values as metric label names.

#### Scenario: Provider returns a sensitive error body
- **WHEN** a provider error body contains token-like or arbitrary content
- **THEN** status records a fixed provider-error reason and sanitized message without the body

### Requirement: Expanded bounded metrics
Prometheus metrics SHALL expose desired/current/drift/conflict counts, incomplete discovery, provider snapshot age, queue depth/retries, plans by phase, applies by outcome, and per-target readiness using bounded labels.

#### Scenario: Metrics cardinality remains bounded
- **WHEN** arbitrary hostnames and Kubernetes object names are reconciled
- **THEN** no metric labels contain hostnames, source object UIDs, provider record IDs, or error strings

### Requirement: Optional operator assets
The Helm chart SHALL optionally render a dashboard ConfigMap and PrometheusRule-compatible alert examples without requiring Prometheus Operator CRDs for a default installation.

#### Scenario: Monitoring assets are disabled
- **WHEN** their Helm values are false
- **THEN** no monitoring-specific custom resources are rendered
