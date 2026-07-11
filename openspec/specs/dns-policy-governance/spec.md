# dns-policy-governance Specification

## Purpose
Defines namespaced, fail-closed DNS publication policy and deterministic quota behavior.

## Requirements
### Requirement: Namespace policy restricts publication
The controller SHALL evaluate all policies matching a source object and SHALL use the restrictive intersection of allowed source kinds, hostname suffixes, TTL bounds, target CIDRs or hostname suffixes, label selectors, and opt-in requirements. A policy SHALL NOT widen global controller or target restrictions.

#### Scenario: Multiple policies intersect
- **WHEN** two matching policies allow different overlapping hostname suffix and TTL ranges
- **THEN** only hostnames and TTLs in the intersection are accepted

#### Scenario: Empty intersection denies publication
- **WHEN** matching policy constraints have no valid intersection
- **THEN** the endpoint is rejected with a bounded policy reason

### Requirement: Deny and invalid policy fail closed
An explicit deny SHALL override allows. Failure to list, parse, or consistently evaluate policy state SHALL mark policy discovery incomplete and suppress cleanup for the affected target.

#### Scenario: Policy API becomes unavailable
- **WHEN** policy enforcement is enabled and the controller cannot complete the policy list
- **THEN** safe creates or exact updates may continue but cleanup is suppressed

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
