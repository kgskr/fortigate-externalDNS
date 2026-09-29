## ADDED Requirements

### Requirement: Release versions are consistent before publishing
The release workflow MUST fail before building or publishing when the release tag version differs from the chart `version`, the chart `appVersion`, or any controller image tag in the raw Deployment manifest.

#### Scenario: Chart version was not bumped
- **WHEN** a `v0.3.2` release is published while `Chart.yaml` or `manifests/deployment.yaml` still names `0.3.1`
- **THEN** the release fails with an error naming each mismatched file before any artifact is built

### Requirement: Private vulnerability reporting
The repository SHALL publish a security policy that directs vulnerability reports to private GitHub Security Advisories and documents token handling for the FortiGate credential.

#### Scenario: Researcher finds a vulnerability
- **WHEN** someone reads the repository security policy
- **THEN** it directs them to a private advisory, states the supported versions, and advises a least-privilege token profile, a dedicated DNS database, and token rotation

## MODIFIED Requirements

### Requirement: Dependency update tracking covers every build-input ecosystem

Dependabot configuration SHALL track `gomod`, `github-actions`, and `docker` ecosystems at least weekly so pinned digests and SHAs are refreshed by automated pull requests, and SHALL group `k8s.io` and `sigs.k8s.io` module updates so tightly coupled Kubernetes libraries move together.

#### Scenario: Base image publishes an update

- **WHEN** an updated digest is published for a pinned Containerfile base image
- **THEN** the next scheduled Dependabot run opens a pull request updating the pinned digest

#### Scenario: Pinned action publishes a release

- **WHEN** a pinned action publishes a new release
- **THEN** the next scheduled Dependabot run opens a pull request updating the commit SHA and its version comment

#### Scenario: Kubernetes libraries release together

- **WHEN** `k8s.io/api`, `k8s.io/apimachinery`, and `k8s.io/client-go` publish a new release
- **THEN** Dependabot proposes them in one grouped pull request

### Requirement: Go toolchain alignment across artifacts

Build and release artifacts SHALL use the same supported Go patch release required by `go.mod`, `go.mod` SHALL name that patch release in its `toolchain` directive, and a toolchain upgrade MUST update the pinned Containerfile builder manifest-list digest and pass the vulnerability gate before publishing.

#### Scenario: Patched Go directive
- **WHEN** the Go vulnerability gate identifies a fixed standard-library patch release
- **THEN** `go.mod` and the Containerfile builder move to that patch release together

#### Scenario: Builder pin verified
- **WHEN** the Containerfile builder tag changes
- **THEN** its multi-architecture manifest-list digest is independently resolved and the container build succeeds before release
