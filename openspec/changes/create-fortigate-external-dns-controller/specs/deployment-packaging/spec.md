## ADDED Requirements

### Requirement: Container image

The project SHALL provide a container image build path for running the controller in Kubernetes.

#### Scenario: Image build requested
- **WHEN** a maintainer builds the project container image
- **THEN** the build produces an image that runs the FortiGate ExternalDNS controller entrypoint

#### Scenario: Controller starts in Kubernetes
- **WHEN** the image is deployed with valid configuration and credentials
- **THEN** the controller starts and begins watching the configured Kubernetes sources

### Requirement: Kubernetes RBAC and manifests

The project SHALL provide Kubernetes manifests that grant only the permissions required for supported sources and deployment operation.

#### Scenario: Core source permissions rendered
- **WHEN** manifests or chart templates are rendered with `Service` and `Ingress` sources enabled
- **THEN** RBAC includes read permissions for those core resources and does not include unrelated service mesh permissions

#### Scenario: Gateway source permissions rendered
- **WHEN** Gateway API support is enabled
- **THEN** RBAC includes read permissions for the supported Gateway API resources

### Requirement: Helm chart

The project SHALL provide a Helm chart for deploying and configuring the controller.

#### Scenario: Helm chart rendered with existing secret
- **WHEN** chart values reference an existing Kubernetes Secret for FortiGate credentials
- **THEN** the rendered Deployment uses that Secret without embedding credential values in the chart output

#### Scenario: Helm chart rendered with source filters
- **WHEN** chart values configure enabled sources, namespace filters, domain filters, owner ID, dry-run, and FortiGate connection options
- **THEN** the rendered Deployment passes those settings to the controller

### Requirement: Public repository documentation

The project SHALL include documentation suitable for a public GitHub repository.

#### Scenario: New user reads README
- **WHEN** a user opens the public repository README
- **THEN** the README explains project purpose, supported resources, unsupported resources, FortiGate-only scope, installation options, configuration, and quick-start examples

#### Scenario: Secret guidance reviewed
- **WHEN** a user follows the public examples
- **THEN** the examples use placeholders or Kubernetes Secret references rather than real FortiGate credentials

### Requirement: License and attribution

The project MUST include license and attribution information compatible with any ExternalDNS-derived code or documentation.

#### Scenario: ExternalDNS-derived code is included
- **WHEN** code or documentation is derived from ExternalDNS
- **THEN** the repository preserves required license notices and attribution before public release

### Requirement: Release validation

The project SHALL include validation commands for tests, chart rendering, and container build checks.

#### Scenario: Maintainer runs validation
- **WHEN** a maintainer runs the documented validation commands
- **THEN** unit tests, Helm rendering checks, and container build checks complete or report actionable errors
