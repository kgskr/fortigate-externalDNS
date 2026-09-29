## MODIFIED Requirements

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
