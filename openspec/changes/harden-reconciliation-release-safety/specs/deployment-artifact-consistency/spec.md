## ADDED Requirements

### Requirement: CA bundle changes roll the workload
The Helm Deployment Pod template SHALL carry a deterministic checksum of the configured FortiGate CA bundle so a changed bundle triggers a rollout.

#### Scenario: CA rotates
- **WHEN** `fortigate.caBundle` changes during a Helm upgrade
- **THEN** the Pod template checksum changes and Kubernetes creates replacement Pods that load the new CA

### Requirement: Positive runtime durations in chart schema
The values schema MUST accept only integer-component Go durations and reject zero or sub-nanosecond values for runtime fields whose application validation requires a value greater than zero.

#### Scenario: Zero interval
- **WHEN** `interval=0s`, `reconcileTimeout=0s`, or `fortigate.timeout=0s` is supplied
- **THEN** Helm schema validation fails before rendering a workload that would CrashLoop

#### Scenario: Sub-nanosecond duration truncates to zero
- **WHEN** a chart duration such as `interval=0.1ns` is supplied
- **THEN** Helm schema validation fails instead of allowing Go to parse it as zero

### Requirement: OpenSpec baseline validation gate
Repository validation SHALL run strict baseline OpenSpec validation so malformed or parser-truncated requirements fail before merge.

#### Scenario: Requirement lacks normative keyword
- **WHEN** a baseline requirement does not contain SHALL or MUST in its parsed text
- **THEN** the validation target and CI fail with the affected specification and requirement

## MODIFIED Requirements

### Requirement: Dry-run default is documented in install steps
Top-level installation documentation in English and Korean SHALL state that the chart defaults to `dryRun: true`, SHALL preview with exclusive-zone acknowledgement while dry-run remains active, and SHALL enable writes only after that equivalent plan is reviewed.

#### Scenario: Operator follows the install guide
- **WHEN** an operator follows the README Helm install section
- **THEN** the section first sets exclusive-zone acknowledgement with `dryRun=true`, directs the operator to review that plan, and only then shows `dryRun=false`

### Requirement: Optional egress NetworkPolicy
The Helm chart SHALL provide an opt-in, disabled-by-default egress NetworkPolicy that denies all egress except DNS resolution, the Kubernetes API endpoint, and the configured FortiGate endpoint, and enabling it MUST require explicit CIDRs for every enabled destination plus at least one Kubernetes API port.

#### Scenario: Egress policy enabled
- **WHEN** the policy is enabled with FortiGate, Kubernetes API, and enabled-DNS CIDRs
- **THEN** the rendered policy selects the controller pod and permits only the configured peers and ports

#### Scenario: Required peer omitted
- **WHEN** the policy is enabled and any required FortiGate, Kubernetes API, or enabled-DNS CIDR is empty
- **THEN** Helm rendering fails instead of producing an all-destination rule

#### Scenario: Kubernetes API ports empty
- **WHEN** the policy is enabled with an empty Kubernetes API port list
- **THEN** values schema validation fails instead of producing an all-port rule

#### Scenario: Disabled by default
- **WHEN** the chart is rendered with default values
- **THEN** no egress NetworkPolicy is rendered and controller egress is unrestricted

### Requirement: Committed-secret scan covers common credential shapes
The committed-credential scan SHALL detect tokens in the credential shapes the project actually ships, including quoted YAML and JSON keys and raw `stringData` token punctuation, and MUST NOT drop a real match merely because the line also contains a common documentation word.

#### Scenario: Quoted token key committed
- **WHEN** a tracked YAML or JSON file contains `"api-token": "<real token>"` or `"FORTIGATE_API_TOKEN": "<real token>"`
- **THEN** the scan flags it

#### Scenario: Raw stringData token committed
- **WHEN** a tracked Secret `stringData` value contains a long token using dots, underscores, or hyphens
- **THEN** the scan flags it rather than assuming only base64-shaped `data` values are credentials

#### Scenario: Token committed in a Secret manifest
- **WHEN** a tracked file contains an API token in a Kubernetes Secret `data` or `stringData` field
- **THEN** the scan flags it rather than excluding the whole line as a placeholder

#### Scenario: Placeholder regression coverage
- **WHEN** maintainers run validation locally or in CI
- **THEN** regression tests exercise quoted keys, placeholder allowlisting, and mixed real-token/placeholder lines

### Requirement: Deployment artifacts stay aligned
Raw manifests, Helm chart templates, README examples, validation documentation, and OpenSpec baseline specifications SHALL describe the same supported runtime options and security posture with shell-safe commands.

#### Scenario: Shell install examples
- **WHEN** a maintainer copies an array-style Helm `--set` command into zsh
- **THEN** the argument is quoted and executes without filename-globbing failure

#### Scenario: Container base documented
- **WHEN** README or validation docs describe the container image
- **THEN** they match the actual Containerfile base and TLS certificate behavior

#### Scenario: Raw manifest reviewed
- **WHEN** raw manifests are provided
- **THEN** they include the same relevant hardening defaults as the Helm chart or explicitly state they are minimal examples
