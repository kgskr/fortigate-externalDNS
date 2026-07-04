# controller-operability Specification

## Purpose
Guarantees the controller runs safely and observably in a cluster: a single writer
reconciles at a time (leader election), each reconcile loop is time-bounded and
cancellable, FortiGate retries respect context cancellation, FortiGate TLS trust is
configurable without disabling verification, health, readiness, and secret-free
metrics endpoints are exposed for Kubernetes probes and scraping, liveness reflects
reconcile-loop progress, and log shape and build identity are operator-configurable.

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

The controller SHALL expose health and readiness endpoints for Kubernetes
probes. Liveness SHALL reflect reconcile-loop progress: while this replica is
responsible for reconciling (it holds leadership, or leader election is
disabled), `/healthz` MUST return non-success when no reconcile attempt has
completed within the configured staleness window. The window SHALL be
configurable (`--healthz-max-staleness`) and SHALL default to a value derived
from the reconcile interval with a safe floor. Replicas not responsible for
reconciling MUST remain live, and reconcile attempts that complete with an
error still count as heartbeat progress.

#### Scenario: Process running and reconciling
- **WHEN** the controller process is running, its HTTP probe server is available, and reconcile attempts are completing within the staleness window
- **THEN** `/healthz` returns success

#### Scenario: Leader loop is wedged
- **WHEN** the replica holds leadership but no reconcile attempt has completed within the staleness window
- **THEN** `/healthz` returns a non-success status so the kubelet restarts the pod

#### Scenario: FortiGate outage does not fail liveness
- **WHEN** reconcile attempts are completing on schedule but failing because the FortiGate device is unreachable
- **THEN** `/healthz` continues to return success, and the failure remains observable through error metrics and the last-successful-reconcile timestamp

#### Scenario: Non-leader pod stays live
- **WHEN** a replica does not hold leadership and therefore performs no reconcile attempts
- **THEN** `/healthz` returns success for that replica

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

### Requirement: FortiGate TLS trust is configurable and fails closed

The controller SHALL accept a PEM CA bundle path (`--fortigate-ca-file` /
`FORTIGATE_CA_FILE`) used as the trust root set for FortiGate TLS
verification, SHALL enforce a minimum TLS version of 1.2 on the FortiGate
client, and configuration validation MUST reject the contradictory combination
of a CA file and `--fortigate-insecure-skip-verify`. An unreadable or
non-PEM CA file MUST fail validation at startup.

#### Scenario: Private-CA device verified
- **WHEN** a CA file containing the device's issuing CA chain is configured and the FortiGate presents a certificate signed by that chain
- **THEN** the TLS connection verifies successfully without `insecure-skip-verify`

#### Scenario: Contradictory trust configuration
- **WHEN** both a CA file and `insecure-skip-verify` are configured
- **THEN** configuration validation fails at startup with an error naming both options

#### Scenario: Malformed CA file
- **WHEN** the configured CA file is missing, unreadable, or contains no PEM certificate
- **THEN** the controller fails at startup with a clear error instead of silently falling back to system roots

#### Scenario: Legacy TLS rejected
- **WHEN** the device offers only TLS 1.1 or lower
- **THEN** the client refuses the connection

### Requirement: Structured logging configuration

The controller SHALL support `--log-format` (`text` or `json`) and
`--log-level` (`debug`, `info`, `warn`, `error`) flags with environment
equivalents, defaulting to the current text/info output. Invalid values MUST
fail configuration validation rather than silently defaulting.

#### Scenario: JSON logs for aggregation
- **WHEN** `--log-format=json` is set
- **THEN** log output is line-delimited JSON produced by the structured logger

#### Scenario: Invalid log configuration
- **WHEN** `LOG_FORMAT=xml` or `--log-level=verbose` is supplied
- **THEN** startup fails with a clear error naming the invalid value

### Requirement: Version identity is reported

The build SHALL stamp a version and commit into the binary; `--version` SHALL
print them and exit successfully without requiring further configuration, and
the metrics endpoint SHALL expose a `build_info` gauge labeled with the
version and commit.

#### Scenario: Version flag
- **WHEN** `fortigate-external-dns --version` is invoked with no other configuration
- **THEN** it prints the stamped version and commit and exits 0

#### Scenario: Running pod correlated to code
- **WHEN** `/metrics` is scraped
- **THEN** the response includes a `build_info` gauge with value 1 carrying version and commit labels

#### Scenario: Release image is stamped
- **WHEN** the release workflow builds the container image for a version tag
- **THEN** the embedded version matches the release tag rather than a development placeholder
