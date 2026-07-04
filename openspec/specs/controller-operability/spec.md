# controller-operability Specification

## Purpose
Guarantees the controller runs safely and observably in a cluster: a single writer
reconciles at a time (leader election), each reconcile loop is time-bounded and
cancellable, FortiGate retries respect context cancellation, and health, readiness,
and secret-free metrics endpoints are exposed for Kubernetes probes and scraping.

## Requirements
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

### Requirement: Startup validation of bind and leader-election configuration

Configuration validation SHALL reject a malformed metrics bind address and a missing leader-election identity at startup rather than failing later in a background goroutine or inside the leader-election machinery.

#### Scenario: Malformed metrics address
- **WHEN** a non-empty `metrics-addr` cannot be parsed as a host:port bind address
- **THEN** configuration validation fails at startup with a clear error naming the value

#### Scenario: Empty leader-election ID with leader election enabled
- **WHEN** leader election is enabled (and the controller is not running with `--once`) but the leader-election ID is empty or whitespace
- **THEN** configuration validation fails at startup with a clear error

### Requirement: Probe and metrics server bind failure is fatal

When a metrics/probe HTTP server is configured, a failure to bind its listener MUST cause the controller to fail loudly rather than continue reconciling while reporting itself ready with no reachable health endpoint.

#### Scenario: Metrics address already in use
- **WHEN** the configured metrics/probe address cannot be bound (for example the port is already in use)
- **THEN** the controller surfaces the bind failure as a fatal startup error and does not report readiness as true with no listener bound

#### Scenario: Shutdown flips readiness
- **WHEN** the controller receives a termination signal
- **THEN** it reports not-ready before tearing down so traffic and probes observe the draining state

### Requirement: Apply-outcome metrics

Operation metrics SHALL distinguish planned operations from applied outcomes so operators can alert on apply failures.

#### Scenario: Apply results recorded
- **WHEN** a reconcile batch applies operations against FortiGate
- **THEN** the operations metric records outcome results (such as applied, failed, skipped, and conflict) in addition to planned, and the metric's documentation matches what is recorded

#### Scenario: Bounded numeric configuration
- **WHEN** `default-ttl` or FortiGate `retries` is set to an obviously out-of-range value
- **THEN** configuration validation rejects it at startup rather than forwarding it to the device or multiplying request latency

### Requirement: Sensitive configuration values are not exposed in help

Configuration loading SHALL support FortiGate API tokens from environment variables
and explicit flags without surfacing secret values in generated CLI help or default
text.

#### Scenario: Token provided by environment
- **WHEN** `FORTIGATE_API_TOKEN` is set and help output is rendered
- **THEN** the help output does not include the token value while runtime configuration still receives the token

#### Scenario: Token flag overrides environment
- **WHEN** both `FORTIGATE_API_TOKEN` and `--fortigate-api-token` are supplied
- **THEN** the explicit flag value wins without making either secret appear as a help default
