## MODIFIED Requirements

### Requirement: Go toolchain alignment across artifacts
Build and release artifacts SHALL use the same supported Go patch release required by `go.mod`, and a toolchain upgrade MUST update the pinned Containerfile builder digest and pass the repository vulnerability gate before publishing.

#### Scenario: Patched Go directive
- **WHEN** the Go vulnerability gate reports a fixed standard-library patch release
- **THEN** `go.mod` and the Containerfile builder move to that patch release together

#### Scenario: Builder pin verified
- **WHEN** the Containerfile builder tag changes
- **THEN** its multi-architecture manifest-list digest is independently resolved and the container build succeeds before release

#### Scenario: Vulnerability gate rerun
- **WHEN** the toolchain is upgraded
- **THEN** `govulncheck` passes for the module before the change is considered complete
