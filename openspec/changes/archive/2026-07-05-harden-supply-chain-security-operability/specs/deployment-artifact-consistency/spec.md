# deployment-artifact-consistency Delta

## ADDED Requirements

### Requirement: Probes are independent of metrics exposure

The Helm chart SHALL render the controller container port, liveness probe, and
readiness probe unconditionally; `metrics.enabled` SHALL gate only scrape
exposure (the metrics Service and its NetworkPolicy). Raw manifests SHALL
carry the same probe wiring.

#### Scenario: Metrics disabled keeps probes

- **WHEN** the chart is rendered with `metrics.enabled=false`
- **THEN** the Deployment still defines the container port and both probes, and no metrics Service is rendered

#### Scenario: Probe timings match liveness semantics

- **WHEN** the chart renders default probe settings
- **THEN** the liveness probe period and failure threshold tolerate the controller's default heartbeat staleness window without false restarts

### Requirement: Optional egress NetworkPolicy

The Helm chart SHALL provide an opt-in, disabled-by-default egress
NetworkPolicy that denies all egress from the controller pod except DNS
resolution, the Kubernetes API endpoint, and the configured FortiGate
endpoint, with the destination ports and peers configurable in values.

#### Scenario: Egress policy enabled

- **WHEN** the egress NetworkPolicy value is enabled with a configured FortiGate address and port
- **THEN** the rendered policy selects the controller pod, allows DNS, the Kubernetes API, and the FortiGate endpoint, and denies other egress

#### Scenario: Disabled by default

- **WHEN** the chart is rendered with default values
- **THEN** no egress NetworkPolicy is rendered and controller egress is unrestricted, matching current behavior

### Requirement: Chart values are schema-validated and documented

The Helm chart SHALL ship a `values.schema.json` covering every supported
value, a chart README documenting each value with its default and purpose, and
a `NOTES.txt` that reports post-install state including whether dry-run mode
is active. Chart validation in CI SHALL render the default and sample values
files against the schema.

#### Scenario: Misspelled value rejected

- **WHEN** a user installs the chart with a value violating the schema (such as a misspelled enum for the cleanup policy or a non-boolean `dryRun`)
- **THEN** Helm fails the install with a schema validation error instead of silently ignoring the value

#### Scenario: Post-install dry-run notice

- **WHEN** the chart is installed with default values
- **THEN** the rendered install notes state that dry-run mode is active and show how to enable writes

#### Scenario: Values documented

- **WHEN** an operator reads the chart README
- **THEN** every value in `values.yaml` appears with its default and a description

### Requirement: Dry-run default is documented in install steps

Top-level installation documentation (English and Korean READMEs) SHALL state
that the chart defaults to `dryRun: true` and SHALL show the explicit step
that enables write mode.

#### Scenario: Operator follows the install guide

- **WHEN** an operator follows the README Helm install section
- **THEN** the section states that no records are written until `dryRun` is set to `false` and shows the command to do so

### Requirement: Token rotation procedure is documented

Because the chart consumes an operator-owned Secret (`existingSecret`) and
cannot roll the pod on Secret changes, the chart README and `values.yaml`
comments SHALL document the rotation procedure (rotate the Secret, then
restart the Deployment) and SHALL note the pod-annotation passthrough for
reloader-style automation.

#### Scenario: Operator rotates the API token

- **WHEN** an operator reads the chart documentation for token rotation
- **THEN** it instructs rotating the referenced Secret and restarting the Deployment (for example `kubectl rollout restart`), and mentions annotation-based reloader integration as an alternative
