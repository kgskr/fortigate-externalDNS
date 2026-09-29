## ADDED Requirements

### Requirement: Chart prevents concurrent writers
The Helm chart MUST fail rendering when more than one replica is requested without leader election, SHALL run one-shot mode as a Job named per Helm revision instead of a Deployment, and SHALL render an opt-in PodDisruptionBudget only for a leader-elected, multi-replica, long-running Deployment.

#### Scenario: Replicas without leader election
- **WHEN** the chart is rendered with `replicaCount=2` and `leaderElection.enabled=false`
- **THEN** rendering fails instead of producing two writers for one database

#### Scenario: One-shot run
- **WHEN** the chart is rendered with `once=true`
- **THEN** it renders a Job with `restartPolicy: Never`, `backoffLimit: 0`, no probes, and a revision-specific name, and no Deployment

#### Scenario: Disruption budget without safe replicas
- **WHEN** the PodDisruptionBudget is enabled for a single replica, without leader election, or with `once=true`
- **THEN** rendering fails

## MODIFIED Requirements

### Requirement: RBAC matches runtime behavior

RBAC manifests SHALL include only permissions required by the configured runtime behavior. Chart and raw platform RBAC MUST grant only the verbs the code uses on each platform resource, SHALL grant status-object permissions whenever target mode is enabled, and SHALL grant EndpointSlice access based on the global headless publication flag.

#### Scenario: Polling implementation
- **WHEN** the controller uses list-based polling and not Kubernetes watches
- **THEN** RBAC does not include unused `watch` verbs unless watch behavior is implemented

#### Scenario: Leader election enabled
- **WHEN** leader election is enabled
- **THEN** RBAC includes the required Lease permissions

#### Scenario: Target mode writes status
- **WHEN** target mode is enabled without the separate status value
- **THEN** RBAC still grants `get` and `create` on status objects and `update` on their status subresource

#### Scenario: Unused verbs are absent
- **WHEN** platform RBAC is rendered
- **THEN** it grants no core event writes, no `patch`, no target status or finalizer writes, and no status-object deletion, while keeping claim `delete` and claim finalizer `update`

#### Scenario: Headless targets applied outside the chart
- **WHEN** headless publication is enabled globally and targets are created with kubectl
- **THEN** EndpointSlice read access is granted without chart-managed target entries

### Requirement: Continuous integration workflows

The repository SHALL provide GitHub Actions workflows that validate the project on push and pull request and publish release artifacts to GHCR only when a GitHub Release is published; workflows MUST NOT embed real credentials and MUST rely on the built-in `GITHUB_TOKEN` for registry authentication. Validation jobs SHALL be time-bounded, and superseded pull-request runs SHALL be cancelled without cancelling default-branch or release-called runs.

#### Scenario: Validation workflow on a pull request
- **WHEN** a pull request is opened
- **THEN** a workflow runs Go tests, `go vet`, gofmt, pinned staticcheck, vulnerability and secret scans, Helm lint/template rendering, and strict baseline OpenSpec validation

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

#### Scenario: Superseded pull request run
- **WHEN** a pull request receives a new push while its previous validation is still running
- **THEN** the previous run is cancelled, while default-branch and release-called runs are never cancelled

### Requirement: Optional egress NetworkPolicy

The Helm chart SHALL provide an opt-in, disabled-by-default egress NetworkPolicy that denies all egress except configured DNS, Kubernetes API, and FortiGate peers. Enabling it MUST require explicit CIDRs for the FortiGate and Kubernetes API peers, a CIDR list or a namespace and pod selector for enabled DNS, and at least one Kubernetes API port. The chart SHALL accept several FortiGate CIDRs and ports while keeping the single-value form compatible.

#### Scenario: Egress policy enabled
- **WHEN** the policy is enabled with FortiGate, Kubernetes API, and enabled-DNS peers
- **THEN** the rendered policy selects the controller pod and permits only the configured peers and ports

#### Scenario: Required peer omitted
- **WHEN** the policy is enabled and the FortiGate or Kubernetes API CIDRs are empty, or enabled DNS has neither CIDRs nor a selector
- **THEN** Helm rendering fails instead of producing an all-destination rule

#### Scenario: Kubernetes API ports empty
- **WHEN** the policy is enabled with an empty Kubernetes API port list
- **THEN** values schema validation fails instead of producing an all-port rule

#### Scenario: Disabled by default
- **WHEN** the chart is rendered with default values
- **THEN** no egress NetworkPolicy is rendered and controller egress is unrestricted, matching current behavior

#### Scenario: Several FortiGate targets
- **WHEN** target mode reconciles more than one FortiGate and their CIDRs and ports are listed
- **THEN** the policy permits each listed FortiGate peer

### Requirement: Chart values are schema-validated and documented

The Helm chart SHALL ship a `values.schema.json` covering every supported value, a chart README documenting each value, and a `NOTES.txt` reporting post-install state; chart validation in CI SHALL render default and sample values against the schema. Direct-mode guards and notes MUST NOT apply in target mode, and optional scheduling values SHALL cover `priorityClassName` and `topologySpreadConstraints`.

#### Scenario: Misspelled value rejected
- **WHEN** a user installs the chart with a value violating the schema (such as a misspelled enum for the cleanup policy or a non-boolean `dryRun`)
- **THEN** Helm fails the install with a schema validation error instead of silently ignoring the value

#### Scenario: Post-install dry-run notice
- **WHEN** the chart is installed with default values
- **THEN** the rendered install notes state that dry-run mode is active and show how to enable writes

#### Scenario: Values documented
- **WHEN** an operator reads the chart README
- **THEN** every value in `values.yaml` appears with its default and a description

#### Scenario: Target mode ignores direct-mode guards
- **WHEN** the chart is rendered in target mode with `dryRun=false` and no exclusive-zone acknowledgement
- **THEN** rendering succeeds and the notes explain that targets carry their own settings and rotation needs no restart

### Requirement: CRDs and least-privilege RBAC are packaged
The repository SHALL package structural `v1alpha1` CRDs for targets, ownership claims, policies, change plans, and status plus the least-privilege namespaced and cluster-scoped RBAC required to watch sources and manage only the controller's own custom resources. The CRDs SHALL validate policy target CIDRs and default target retries to 2, and the documentation MUST tell operators to apply the CRDs from the matching release before a chart upgrade because Helm does not upgrade them.

#### Scenario: Default Helm render includes CRDs safely
- **WHEN** the chart is installed with default values
- **THEN** structural CRDs and required RBAC render while shared-zone, multi-target, policy enforcement, and source expansion remain disabled

#### Scenario: RBAC creation is disabled
- **WHEN** `rbac.create=false`
- **THEN** the chart renders no RBAC and documents every permission the operator must supply

#### Scenario: Chart upgrade with changed CRDs
- **WHEN** an operator upgrades to a release whose CRDs changed
- **THEN** the documented procedure applies that release's CRDs with server-side apply before `helm upgrade`

### Requirement: Go toolchain alignment across artifacts

Build and release artifacts SHALL use the same supported Go patch release required by `go.mod`, `go.mod` SHALL carry a `toolchain` directive naming that patch release, and an upgrade MUST update the pinned Containerfile builder manifest-list digest and pass the vulnerability gate before publishing.

#### Scenario: Go directive is upgraded
- **WHEN** `go.mod` requires a newer Go version
- **THEN** the Containerfile builder image and CI setup use a compatible version before image publishing is allowed

#### Scenario: CI resolves the pinned toolchain
- **WHEN** CI sets up Go from `go.mod`
- **THEN** it uses the `toolchain` patch release that the Containerfile builder uses
