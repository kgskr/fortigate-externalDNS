## ADDED Requirements

### Requirement: Gateway target namespace lookup

Gateway API target lookup SHALL support explicitly configured namespaces that are separate from source ownership and cleanup scope.

#### Scenario: Shared infrastructure Gateway
- **WHEN** an HTTPRoute in an application namespace references a parent Gateway in a configured target namespace
- **THEN** the controller can resolve the Gateway address and publish the route hostname

#### Scenario: Target namespace not configured
- **WHEN** an HTTPRoute references a parent Gateway outside the configured source and target namespaces
- **THEN** the controller does not publish a record for that parent and reports the missing target scope

### Requirement: Cleanup scope remains ownership-scoped

Gateway target lookup namespaces MUST NOT expand stale record cleanup ownership scope.

#### Scenario: Infra namespace used only for target lookup
- **WHEN** a shared Gateway namespace is configured only as a target lookup namespace
- **THEN** stale record cleanup does not delete records sourced from that namespace unless it is also within cleanup scope

### Requirement: Accepted Gateway API parent matching

HTTPRoute publishing SHALL use targets only from Gateway parent references whose full parent identity is accepted and has resolved references.

#### Scenario: Mixed accepted and rejected parents
- **WHEN** a route has one accepted Gateway parent and one rejected Gateway parent
- **THEN** only the accepted Gateway target contributes to desired DNS records

#### Scenario: Accepted non-Gateway parent
- **WHEN** a route status marks a non-Gateway parent as accepted
- **THEN** that status does not authorize publishing targets from a Gateway with the same name

### Requirement: Explicit Service publish policy

Service source behavior SHALL report unsupported or unpublished Service types instead of silently ignoring them.

#### Scenario: ClusterIP Service with hostname
- **WHEN** a ClusterIP Service has a supported hostname annotation but the configured Service publish policy does not include ClusterIP
- **THEN** the controller emits an event or log explaining that the Service type is not published

#### Scenario: Supported LoadBalancer Service
- **WHEN** a LoadBalancer Service has a hostname and publishable status address
- **THEN** the controller publishes the corresponding desired DNS record as before
