## ADDED Requirements

### Requirement: Released artifacts are digest signed
Every published container image digest and Helm chart archive SHALL receive a keyless Cosign signature bound to the expected GitHub repository, release workflow identity, and OIDC issuer. Tags alone SHALL NOT be signed or documented as immutable evidence.

#### Scenario: Release signing succeeds
- **WHEN** the trusted release workflow publishes an image and chart
- **THEN** signatures for their immutable digests are discoverable and verifiable using documented identity and issuer constraints

### Requirement: SPDX SBOMs accompany releases
The release workflow SHALL generate SPDX JSON SBOMs for the final container image and packaged chart, attach them to the release, and associate image evidence with the immutable digest.

#### Scenario: SBOM generation fails
- **WHEN** either required SBOM cannot be generated or attached
- **THEN** release publication fails closed

### Requirement: Provenance is verifiable
Published image and chart artifacts SHALL have SLSA-compatible provenance identifying source repository, commit, workflow, build inputs, and resulting digest, generated through GitHub OIDC without a long-lived signing key.

#### Scenario: Artifact does not match provenance
- **WHEN** verification is attempted against modified artifact bytes or a different digest
- **THEN** verification fails

### Requirement: Pull requests do not publish trust evidence
Pull-request CI SHALL validate signing and provenance configuration without publishing signatures, release assets, images, or attestations.

#### Scenario: Untrusted pull request runs
- **WHEN** CI runs for a fork or pull request
- **THEN** it receives no release signing authority and cannot publish trusted evidence
