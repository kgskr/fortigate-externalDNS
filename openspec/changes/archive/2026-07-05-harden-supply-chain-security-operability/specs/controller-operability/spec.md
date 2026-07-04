# controller-operability Delta

## MODIFIED Requirements

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

## ADDED Requirements

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
