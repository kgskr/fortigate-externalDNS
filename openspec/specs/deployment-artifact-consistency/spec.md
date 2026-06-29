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

### Requirement: Gateway target namespace RBAC in namespaced mode

When the Helm chart is configured with explicit source namespaces, it SHALL also grant the controller read access to Gateway API resources in every configured gateway-target namespace, so the gateway-target-namespace feature does not fail with a `forbidden` error.

#### Scenario: Namespaced mode with gateway target namespaces
- **WHEN** the chart is rendered with non-empty `namespaces` and non-empty `gatewayTargetNamespaces` and the gateway source enabled
- **THEN** the rendered RBAC includes a Role/RoleBinding granting `get`/`list` on `gateways` and `httproutes` in each gateway-target namespace

#### Scenario: Cluster-wide mode unaffected
- **WHEN** the chart is rendered with empty `namespaces`
- **THEN** the cluster-scoped RBAC continues to authorize gateway-target lookups as before

### Requirement: Least-privilege leader-election lease scoping

Leader-election RBAC SHALL restrict `get` and `update` on the lease to the specific lease name via `resourceNames`, granting only `create` namespace-wide.

#### Scenario: Lease RBAC rendered
- **WHEN** RBAC for leader election is generated for the chart or raw manifests
- **THEN** `get` and `update` on `coordination.k8s.io` leases are limited to the configured leader-election lease name, so a compromised token cannot seize an unrelated component's lease

### Requirement: License text matches asserted license

The bundled `LICENSE` file SHALL contain the verbatim text of the license that the README and `NOTICE` assert the project uses.

#### Scenario: License asserted as Apache 2.0
- **WHEN** the README and `NOTICE` state the project uses the Apache License 2.0
- **THEN** the `LICENSE` file contains the verbatim official Apache License 2.0 text, including its appendix

### Requirement: Cleanup policy is documented

User-facing documentation SHALL describe the `--cleanup-policy` option, its default, and the data-safety implications of each mode.

#### Scenario: Operator reads configuration docs
- **WHEN** an operator reads the README configuration or operability documentation
- **THEN** it documents `--cleanup-policy` / `CLEANUP_POLICY` with its `delete` default and the `deactivate` and `keep` alternatives, noting that `delete` is destructive

### Requirement: Committed-secret scan covers common credential shapes

The committed-credential scan SHALL detect tokens in the credential shapes the project actually ships and MUST NOT drop a real match merely because the line also contains a common documentation word.

#### Scenario: Token committed in a Secret manifest
- **WHEN** a tracked file contains an API token in a Kubernetes Secret `data`/`stringData` field or a token-shaped value on a line that also contains a placeholder word such as `example`
- **THEN** the scan flags it rather than excluding the whole line as a placeholder

