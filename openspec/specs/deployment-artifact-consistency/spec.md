# deployment-artifact-consistency Specification

## Purpose
Keeps the shipped deployment artifacts (Helm chart, raw manifests, Containerfile,
CI/CD workflows, and docs) mutually consistent and secure: RBAC matches the
controller's actual runtime access, the container image is hardened and referenced
consistently, continuous integration validates source changes separately from release
publishing, release artifacts are published to GHCR only from GitHub Releases without
embedded credentials, and validation documentation stays runnable.

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

### Requirement: Continuous integration workflows

The repository SHALL provide GitHub Actions workflows that validate the project
on push and pull request and publish release artifacts (container image and Helm
chart) to GHCR only when a GitHub Release is published. Workflows MUST NOT embed
real credentials and MUST rely on the built-in `GITHUB_TOKEN` for registry
authentication.

#### Scenario: Validation workflow on a pull request
- **WHEN** a pull request is opened
- **THEN** a workflow runs the Go tests, `go vet`, gofmt, the secret scan, and a Helm lint/template render

#### Scenario: Validation workflow on default branch push
- **WHEN** a commit is pushed to the default branch
- **THEN** a workflow runs validation checks but does not publish container images or Helm charts

#### Scenario: Release published from a version tag
- **WHEN** a GitHub Release is published for a `v*` tag
- **THEN** a workflow gates publishing on the reusable CI validation workflow, builds the multi-arch container image, pushes semver and latest image tags to `ghcr.io/<owner>/fortigate-external-dns`, packages the Helm chart, and pushes it to GHCR as an OCI artifact

#### Scenario: Version tag push alone
- **WHEN** a `v*` tag is pushed but no GitHub Release has been published
- **THEN** release artifact publishing does not run

#### Scenario: No committed credentials in workflows
- **WHEN** the workflows are reviewed
- **THEN** they contain no hardcoded tokens and the secret scan passes over them

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

#### Scenario: Placeholder regression coverage
- **WHEN** maintainers run validation locally or in CI
- **THEN** the secret scan regression test exercises placeholder allowlisting and mixed real-token/placeholder lines so future edits do not reintroduce line-level allowlist bypasses

### Requirement: Go toolchain alignment across artifacts

Build and release artifacts SHALL use a Go toolchain version compatible with
`go.mod`, including local binary builds, the Containerfile builder stage, and CI.

#### Scenario: Go directive is upgraded
- **WHEN** `go.mod` requires a newer Go version
- **THEN** the Containerfile builder image and CI setup use a compatible version before image publishing is allowed
