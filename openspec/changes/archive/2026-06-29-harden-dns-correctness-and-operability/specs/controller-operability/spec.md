## ADDED Requirements

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
