# supply-chain-security Specification

## Purpose
Bounds the project's supply-chain risk and makes "a patch is needed" an
automated signal rather than maintainer vigilance: build inputs (base images,
workflow actions) are pinned to immutable identifiers, every input ecosystem is
tracked for updates, vulnerability scanning gates validation before release,
and published artifacts are rescanned on a schedule with a loud failure
channel.

## Requirements
### Requirement: Build inputs are pinned to immutable identifiers

Container base images referenced by the Containerfile MUST be pinned to their multi-architecture manifest-list digest with the tag retained as context, and every GitHub Actions `uses` step MUST be pinned to a full commit SHA with a version comment.

#### Scenario: Base image referenced by digest

- **WHEN** the Containerfile builder or runtime stage references a base image
- **THEN** the reference includes an `@sha256:` manifest-list digest and the multi-arch build (`--platform` cross-compile) still succeeds

#### Scenario: Action referenced by SHA

- **WHEN** a workflow step uses a third-party or GitHub-owned action
- **THEN** the `uses:` reference is a 40-character commit SHA followed by a comment naming the semantic version

#### Scenario: Mutable reference introduced in review

- **WHEN** a change proposes a base image without a digest or an action pinned only by tag or branch
- **THEN** validation documentation identifies digest/SHA pinning as required so the regression is caught in review

### Requirement: Dependency update tracking covers every build-input ecosystem

Dependabot configuration SHALL track `gomod`, `github-actions`, and `docker` ecosystems at least weekly so pinned digests and SHAs are refreshed by automated pull requests.

#### Scenario: Base image publishes an update

- **WHEN** an updated digest is published for a pinned Containerfile base image
- **THEN** the next scheduled Dependabot run opens a pull request updating the pinned digest

#### Scenario: Pinned action publishes a release

- **WHEN** a pinned action publishes a new release
- **THEN** the next scheduled Dependabot run opens a pull request updating the commit SHA and its version comment

### Requirement: Vulnerability scanning gates validation

CI validation SHALL run `govulncheck`, SHALL build and scan the container image with fixable HIGH or CRITICAL findings treated as failures, and MUST keep these checks in the reusable workflow and on pull requests so release publishing and merges are gated.

#### Scenario: Reachable Go vulnerability

- **WHEN** the Go vulnerability database reports a vulnerability reachable from the module's code or present in the stdlib at the toolchain version
- **THEN** the CI govulncheck step fails the validation workflow

#### Scenario: Fixable HIGH finding in the built image

- **WHEN** the image scanner reports a HIGH or CRITICAL vulnerability with an available fix in the image built from the pull request
- **THEN** the validation workflow fails, and accepted findings are only suppressible through a tracked ignore file

#### Scenario: Containerfile builder cannot compile the module

- **WHEN** a pull request makes the pinned builder image incompatible with `go.mod` (or otherwise breaks the container build)
- **THEN** the pull request's validation workflow fails at the image build step rather than the breakage surfacing after merge

#### Scenario: Release gating unchanged

- **WHEN** a GitHub Release triggers publishing
- **THEN** publishing remains gated on the same reusable validation workflow, now including the scan steps

### Requirement: Published artifacts are rescanned on a schedule

A scheduled workflow SHALL run at least weekly to rerun `govulncheck` and rescan the latest published image, and on any finding MUST fail and create or update a labeled GitHub issue.

#### Scenario: CVE published after release

- **WHEN** a vulnerability affecting the latest published image is disclosed after the release was built
- **THEN** the next scheduled run fails and an open issue labeled for security scanning exists describing the finding

#### Scenario: Repeated failing runs deduplicate

- **WHEN** consecutive scheduled runs fail while a previous scan issue is still open
- **THEN** the workflow updates the existing open issue instead of filing duplicates

#### Scenario: Least-privilege schedule permissions

- **WHEN** the scheduled workflow runs
- **THEN** its token permissions are limited to reading contents and writing issues

### Requirement: Go toolchain alignment across artifacts

Build and release artifacts SHALL use the same supported Go patch release required by `go.mod`, and a toolchain upgrade MUST update the pinned Containerfile builder manifest-list digest and pass the vulnerability gate before publishing.

#### Scenario: Patched Go directive
- **WHEN** the Go vulnerability gate identifies a fixed standard-library patch release
- **THEN** `go.mod` and the Containerfile builder move to that patch release together

#### Scenario: Builder pin verified
- **WHEN** the Containerfile builder tag changes
- **THEN** its multi-architecture manifest-list digest is independently resolved and the container build succeeds before release
