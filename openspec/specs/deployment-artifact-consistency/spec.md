# deployment-artifact-consistency Specification

## Purpose
TBD - created by archiving change harden-dns-reconciliation-safety. Update Purpose after archive.
## Requirements
### Requirement: Deployment artifacts stay aligned

Raw manifests, Helm chart templates, README examples, and validation documentation SHALL describe the same supported runtime options and security posture.

#### Scenario: Container base documented
- **WHEN** README or validation docs describe the container image
- **THEN** they match the actual Containerfile base and TLS certificate behavior

#### Scenario: Raw manifest reviewed
- **WHEN** raw manifests are provided
- **THEN** they include the same relevant hardening defaults as the Helm chart or explicitly state they are minimal examples

### Requirement: RBAC matches runtime behavior

RBAC manifests SHALL include only permissions required by the configured runtime behavior.

#### Scenario: Polling implementation
- **WHEN** the controller uses list-based polling and not Kubernetes watches
- **THEN** RBAC does not include unused `watch` verbs unless watch behavior is implemented

#### Scenario: Leader election enabled
- **WHEN** leader election is enabled
- **THEN** RBAC includes the required Lease permissions

### Requirement: No GitHub workflow files

This change MUST NOT add GitHub Actions workflow files.

#### Scenario: Repository checked after change
- **WHEN** the change is complete
- **THEN** `.github/workflows` contains no workflow files added by this change

### Requirement: Validation documentation

Validation documentation SHALL include local commands for tests, static checks, Helm rendering, manifest checks, and container build checks.

#### Scenario: Maintainer follows validation docs
- **WHEN** a maintainer runs the documented local validation commands
- **THEN** the commands either pass or produce actionable local-environment errors

### Requirement: Restricted-PSS deployment hardening

Helm chart and raw manifests SHALL provide security defaults compatible with the Kubernetes restricted Pod Security Standard plus recommended hardening, and the container image SHALL be referenced by an immutable identifier or a documented pinning policy.

#### Scenario: Restricted PSS namespace
- **WHEN** the chart is installed into a namespace enforcing the restricted Pod Security Standard
- **THEN** the rendered Deployment runs non-root, sets `allowPrivilegeEscalation: false`, drops all capabilities, and sets `seccompProfile` to `RuntimeDefault`, and is admitted without policy violations

#### Scenario: Recommended hardening applied
- **WHEN** the chart renders the controller container with default values
- **THEN** it sets `readOnlyRootFilesystem: true` and default resource requests and limits

#### Scenario: Image reference is pinned
- **WHEN** the chart or raw manifests reference the controller image
- **THEN** the image is pinned by digest or follows a documented tag-pinning policy rather than a mutable `latest` tag

### Requirement: Dead code cleanup

Unused exported helpers added by the initial implementation SHALL be removed or connected to tested behavior.

#### Scenario: Dead helper scan
- **WHEN** the codebase is searched for helper usage
- **THEN** functions such as unused redaction, sorting, or mutable-operation helpers are either used by production/tests or removed

