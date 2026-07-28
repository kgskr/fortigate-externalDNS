# deployment-artifact-consistency Specification

## Purpose
Keeps the shipped deployment artifacts (Helm chart, raw manifests, Containerfile,
CI/CD workflows, and docs) mutually consistent and secure: RBAC matches the
controller's actual runtime access, the container image is hardened and referenced
consistently, continuous integration validates source changes separately from release
publishing, release artifacts are published to GHCR only from GitHub Releases without
embedded credentials, the chart's operational surface (probes, values schema and
docs, egress policy, CA trust, token rotation) is production-ready by default, and
validation documentation stays runnable.

## Requirements
### Requirement: Deployment artifacts stay aligned

Raw manifests, Helm chart templates, README examples, validation documentation, and baseline specifications SHALL describe the same supported runtime options and security posture with shell-safe commands.

#### Scenario: Container base documented
- **WHEN** README or validation docs describe the container image
- **THEN** they match the actual Containerfile base and TLS certificate behavior

#### Scenario: Raw manifest reviewed
- **WHEN** raw manifests are provided
- **THEN** they include the same relevant hardening defaults as the Helm chart or explicitly state they are minimal examples

#### Scenario: Shell install examples
- **WHEN** an array-style Helm `--set` command is copied into zsh
- **THEN** the argument is quoted and executes without filename-globbing failure

### Requirement: RBAC matches runtime behavior

RBAC manifests SHALL include only permissions required by the configured runtime behavior.

#### Scenario: Polling implementation
- **WHEN** the controller uses list-based polling and not Kubernetes watches
- **THEN** RBAC does not include unused `watch` verbs unless watch behavior is implemented

#### Scenario: Leader election enabled
- **WHEN** leader election is enabled
- **THEN** RBAC includes the required Lease permissions

### Requirement: Existing ServiceAccount selection is explicit

When ServiceAccount creation is disabled, the Helm chart MUST require a non-empty existing ServiceAccount name and MUST NOT bind the workload or RBAC to the namespace's `default` ServiceAccount implicitly.

#### Scenario: ServiceAccount creation disabled without a name
- **WHEN** `serviceAccount.create=false` and `serviceAccount.name` is empty
- **THEN** schema validation or template rendering fails before deployment

### Requirement: Continuous integration workflows

The repository SHALL provide GitHub Actions workflows that validate the project on push and pull request and publish release artifacts to GHCR only when a GitHub Release is published; workflows MUST NOT embed real credentials and MUST rely on the built-in `GITHUB_TOKEN` for registry authentication.

#### Scenario: Validation workflow on a pull request
- **WHEN** a pull request is opened
- **THEN** a workflow runs Go tests, `go vet`, gofmt, vulnerability and secret scans, Helm lint/template rendering, and strict baseline OpenSpec validation

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

The committed-credential scan SHALL detect tokens in the credential shapes the project actually ships, including quoted YAML and JSON keys and raw `stringData` token punctuation, and MUST NOT drop a real match merely because the line also contains a common documentation word.

#### Scenario: Quoted token key committed
- **WHEN** a tracked YAML or JSON file contains `"api-token": "<real token>"` or `"FORTIGATE_API_TOKEN": "<real token>"`
- **THEN** the scan flags it

#### Scenario: Raw stringData token committed
- **WHEN** a tracked Secret `stringData` value contains a long token using dots, underscores, or hyphens
- **THEN** the scan flags it rather than assuming only base64-shaped `data` values are credentials

#### Scenario: Token committed in a Secret manifest
- **WHEN** a tracked file contains an API token in a Kubernetes Secret `data`/`stringData` field or a token-shaped value on a line that also contains a placeholder word such as `example`
- **THEN** the scan flags it rather than excluding the whole line as a placeholder

#### Scenario: Placeholder regression coverage
- **WHEN** maintainers run validation locally or in CI
- **THEN** the secret scan regression test exercises quoted keys, placeholder allowlisting, and mixed real-token/placeholder lines

### Requirement: Go toolchain alignment across artifacts

Build and release artifacts SHALL use the same supported Go patch release required by `go.mod`, and an upgrade MUST update the pinned Containerfile builder manifest-list digest and pass the vulnerability gate before publishing.

#### Scenario: Go directive is upgraded
- **WHEN** `go.mod` requires a newer Go version
- **THEN** the Containerfile builder image and CI setup use a compatible version before image publishing is allowed

### Requirement: Probes are independent of metrics exposure

The Helm chart SHALL render the controller container port, liveness probe, and readiness probe unconditionally, `metrics.enabled` SHALL gate only scrape exposure, and raw manifests SHALL carry the same probe wiring.

#### Scenario: Metrics disabled keeps probes
- **WHEN** the chart is rendered with `metrics.enabled=false`
- **THEN** the Deployment still defines the container port and both probes, and no metrics Service is rendered

#### Scenario: Probe timings match liveness semantics
- **WHEN** the chart renders default probe settings
- **THEN** the liveness probe period and failure threshold tolerate the controller's default heartbeat staleness window without false restarts

### Requirement: Optional egress NetworkPolicy

The Helm chart SHALL provide an opt-in, disabled-by-default egress NetworkPolicy that denies all egress except configured DNS, Kubernetes API, and FortiGate peers, and enabling it MUST require explicit CIDRs for every enabled destination plus at least one Kubernetes API port.

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
- **THEN** no egress NetworkPolicy is rendered and controller egress is unrestricted, matching current behavior

### Requirement: Chart values are schema-validated and documented

The Helm chart SHALL ship a `values.schema.json` covering every supported value, a chart README documenting each value, and a `NOTES.txt` reporting post-install state; chart validation in CI SHALL render default and sample values against the schema.

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

Top-level installation documentation in English and Korean SHALL state that the chart defaults to `dryRun: true`, SHALL preview with exclusive-zone acknowledgement while dry-run remains active, and SHALL enable writes only after that equivalent plan is reviewed.

#### Scenario: Operator follows the install guide
- **WHEN** an operator follows the README Helm install section
- **THEN** the section first sets exclusive-zone acknowledgement with `dryRun=true`, directs the operator to review that plan, and only then shows `dryRun=false`

### Requirement: Token rotation procedure is documented

The chart README and `values.yaml` comments SHALL document that an operator-owned Secret change requires a Deployment restart and SHALL note the pod-annotation passthrough for reloader-style automation.

#### Scenario: Operator rotates the API token
- **WHEN** an operator reads the chart documentation for token rotation
- **THEN** it instructs rotating the referenced Secret and restarting the Deployment (for example `kubectl rollout restart`), and mentions annotation-based reloader integration as an alternative

### Requirement: CA bundle changes roll the workload

The Helm Deployment Pod template SHALL carry a deterministic checksum of the configured FortiGate CA bundle so a changed bundle triggers a rollout.

#### Scenario: CA rotates
- **WHEN** `fortigate.caBundle` changes during a Helm upgrade
- **THEN** the Pod template checksum changes and Kubernetes creates replacement Pods that load the new CA

### Requirement: Positive runtime durations in chart schema

The values schema MUST accept only integer-component Go durations and reject zero or sub-nanosecond values for runtime fields whose application validation requires a value greater than zero.

#### Scenario: Zero runtime duration
- **WHEN** `interval=0s`, `reconcileTimeout=0s`, or `fortigate.timeout=0s` is supplied
- **THEN** Helm schema validation fails before rendering a workload that would fail startup

#### Scenario: Sub-nanosecond duration truncates to zero
- **WHEN** a chart duration such as `interval=0.1ns` is supplied
- **THEN** Helm schema validation fails instead of allowing Go to parse it as zero

### Requirement: OpenSpec baseline validation gate

Repository validation SHALL run strict baseline OpenSpec validation so malformed or parser-truncated requirements fail before merge.

#### Scenario: Requirement lacks normative keyword
- **WHEN** a baseline requirement does not contain SHALL or MUST in its parsed text
- **THEN** the validation target and CI fail with the affected specification and requirement

### Requirement: CRDs and least-privilege RBAC are packaged
The repository SHALL package structural `v1alpha1` CRDs for targets, ownership claims, policies, change plans, and status plus the least-privilege namespaced and cluster-scoped RBAC required to watch sources and manage only the controller's own custom resources.

#### Scenario: Default Helm render includes CRDs safely
- **WHEN** the chart is installed with default values
- **THEN** structural CRDs and required RBAC render while shared-zone, multi-target, policy enforcement, and source expansion remain disabled

#### Scenario: RBAC creation is disabled
- **WHEN** `rbac.create=false`
- **THEN** the chart renders no RBAC and documents every permission the operator must supply

### Requirement: Compatibility-safe Helm values and schema
Helm values SHALL expose explicit enablement and configuration for plan approval, shared ownership, policy, event debounce/resync, targets, source expansion, status retention, and monitoring assets. The JSON schema SHALL reject contradictory modes, invalid Secret references, unbounded retention, negative durations, and unsafe overlapping inline targets where statically detectable.

#### Scenario: Existing values render unchanged
- **WHEN** an existing single-target values file is rendered after upgrade
- **THEN** it continues to produce a legacy default target and no new write mode is enabled

### Requirement: Raw manifests remain a supported legacy path
Raw manifests SHALL remain deployable for the compatibility single-target mode and SHALL include required EndpointSlice/watch permissions only when the corresponding feature is enabled or documented as an operator patch.

#### Scenario: Raw manifest validation
- **WHEN** committed raw manifests are parsed and inspected by repository checks
- **THEN** they contain no plaintext token and retain dry-run and exclusive-ownership safety defaults

### Requirement: Generated and documented artifacts stay synchronized
CI SHALL validate CRD structural schemas, Helm schema and renders across representative feature combinations, RBAC permissions, generated dashboards/rules, raw YAML parsing, and documentation examples.

#### Scenario: CRD or values drift occurs
- **WHEN** code expects a field absent from the committed CRD or values schema
- **THEN** repository validation fails before release
