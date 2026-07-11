## ADDED Requirements

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
