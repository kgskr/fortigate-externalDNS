# dns-policy-governance Specification

## Purpose
Defines namespaced, fail-closed DNS publication policy and deterministic quota behavior.
## Requirements
### Requirement: Namespace policy restricts publication
The controller SHALL evaluate all policies matching a source object and SHALL use the restrictive intersection of allowed source kinds, hostname suffixes, TTL bounds, target CIDRs or hostname suffixes, label selectors, and opt-in requirements. A policy SHALL NOT widen global controller or target restrictions. When a policy restricts only target CIDRs, hostname targets MUST be denied, and when it restricts only target hostname suffixes, IP targets MUST be denied.

#### Scenario: Multiple policies intersect
- **WHEN** two matching policies allow different overlapping hostname suffix and TTL ranges
- **THEN** only hostnames and TTLs in the intersection are accepted

#### Scenario: Empty intersection denies publication
- **WHEN** matching policy constraints have no valid intersection
- **THEN** the endpoint is rejected with a bounded policy reason

#### Scenario: Target kind outside the restricted kind
- **WHEN** a policy sets only `allowedTargetCIDRs` and a source resolves to a CNAME target, or sets only `allowedTargetSuffixes` and a source resolves to an IP target
- **THEN** the endpoint is rejected with the target-not-allowed reason instead of bypassing the restriction

### Requirement: Deny and invalid policy fail closed
An explicit deny SHALL override allows. Failure to list or decode configured policy state MUST block every provider mutation for the affected target until a complete policy snapshot is available. A policy that fails to compile SHALL deny publication for every source in its namespace with a fixed `PolicyInvalid` reason, and while any policy is invalid the controller MUST suppress all cleanup for the target; sources in other namespaces SHALL continue to publish.

#### Scenario: Policy API becomes unavailable
- **WHEN** policy enforcement is enabled and the controller cannot complete the policy list
- **THEN** create, update, replace, delete, and deactivate operations are all withheld for that reconciliation

#### Scenario: One namespace has an invalid policy
- **WHEN** a policy in one namespace has a malformed CIDR, selector, or TTL range
- **THEN** that namespace's candidates are rejected with `PolicyInvalid`, its existing records are not deleted or deactivated, the plan records the policy precondition as incomplete, and other namespaces still publish

### Requirement: Explicit publication opt-in
Policy SHALL be able to require an exact opt-in annotation on source objects before any hostname is published.

#### Scenario: Required opt-in is absent
- **WHEN** a matching policy requires opt-in and a source object lacks the configured annotation value
- **THEN** the source publishes no desired endpoint and receives a warning event and status reason

### Requirement: Per-namespace and per-target quotas
Policy SHALL support deterministic maximum desired logical records per namespace and per target. Quota evaluation SHALL occur after endpoint normalization and conflict detection and before planning.

#### Scenario: Quota is exceeded
- **WHEN** accepted normalized endpoints exceed a configured quota
- **THEN** the controller rejects the excess set deterministically, emits no mutation for it, and reports the quota condition without unbounded metric labels

### Requirement: Compatibility when governance is disabled
With policy enforcement disabled and no policy objects, existing source, domain, namespace, and TTL behavior SHALL remain unchanged.

#### Scenario: Upgrade without policy objects
- **WHEN** an existing release upgrades with default values
- **THEN** no endpoint is newly rejected for absence of a policy or opt-in annotation

