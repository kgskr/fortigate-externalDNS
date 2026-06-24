## ADDED Requirements

### Requirement: Single-writer reconciliation

The controller SHALL support Kubernetes Lease-based leader election or an equivalent single-writer guard for in-cluster deployments.

#### Scenario: Multiple replicas running
- **WHEN** two controller pods are deployed with leader election enabled
- **THEN** only the elected leader performs FortiGate reconciliation

#### Scenario: Non-leader pod healthy
- **WHEN** a pod is not the current leader
- **THEN** it remains live but does not apply DNS changes

### Requirement: Reconcile timeout

Each reconciliation loop SHALL run with a configured timeout that bounds Kubernetes list calls and FortiGate API calls.

#### Scenario: Kubernetes list hangs
- **WHEN** a Kubernetes API call exceeds the reconcile timeout
- **THEN** the reconcile loop is canceled and a later loop can retry

### Requirement: Context-aware FortiGate retry

FortiGate retry backoff MUST respect context cancellation.

#### Scenario: Context canceled during retry sleep
- **WHEN** the reconciliation context is canceled while waiting between retries
- **THEN** retry sleep exits promptly and the operation returns the context error

### Requirement: Health and readiness endpoints

The controller SHALL expose health and readiness endpoints for Kubernetes probes.

#### Scenario: Process running
- **WHEN** the controller process is running and its HTTP probe server is available
- **THEN** `/healthz` returns success

#### Scenario: Controller not ready
- **WHEN** required clients or configuration are not ready
- **THEN** `/readyz` returns a non-success status

### Requirement: Metrics endpoint

The controller SHALL expose operational metrics without leaking secrets.

#### Scenario: Metrics scraped
- **WHEN** `/metrics` is requested
- **THEN** the response includes reconcile counts, duration, operation counts, and error counts without FortiGate tokens or secret values
