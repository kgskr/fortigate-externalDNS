# source-publishing-scope Specification

## Purpose
Defines which Kubernetes resources and hostnames become DNS records and bounds that
scope: only in-zone, non-wildcard hostnames are published; a name never gets both an
address and a CNAME; Gateway target-namespace lookup does not widen ownership or
cleanup; HTTPRoute targets come only from accepted, resolved Gateway parents; and
unpublished Service types are reported rather than silently ignored.

## Requirements
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

Service source behavior SHALL publish only LoadBalancer status addresses and Service `ExternalIPs`, and SHALL report any other Service type as unpublished instead of silently ignoring it. There is no configurable per-type policy; the supported set is fixed.

#### Scenario: ClusterIP Service with hostname
- **WHEN** a ClusterIP Service has a supported hostname annotation
- **THEN** the controller emits an event or log explaining that the Service type is not published (only LoadBalancer status addresses and ExternalIPs are)

#### Scenario: Supported LoadBalancer Service
- **WHEN** a LoadBalancer Service has a hostname and publishable status address
- **THEN** the controller publishes the corresponding desired DNS record as before

### Requirement: Published hostnames must be within the configured zone

The source layer SHALL only publish a desired record when the hostname is equal to, or a subdomain of, the configured FortiGate zone. A hostname outside the zone MUST NOT be written into the zone's `dns-database`.

#### Scenario: Out-of-zone hostname
- **WHEN** a Service, Ingress, Gateway, or HTTPRoute declares a hostname that is neither the configured zone nor a subdomain of it
- **THEN** the controller does not create a record for that hostname and emits a warning event explaining it is outside the configured zone

#### Scenario: In-zone hostname
- **WHEN** a resource declares a hostname equal to or a subdomain of the configured zone
- **THEN** the controller publishes the corresponding desired record as before

### Requirement: Wildcard hostnames are not published

The source layer SHALL NOT publish a hostname whose leftmost label is a wildcard (`*`), because the FortiGate `dns-database` `hostname` field does not accept a leading wildcard label.

#### Scenario: Wildcard listener or ingress host
- **WHEN** a Gateway listener or Ingress rule declares a hostname beginning with `*.`
- **THEN** the controller skips that hostname and emits a warning event that wildcard records are unsupported, instead of attempting to create a malformed entry every reconcile

### Requirement: Status targets do not produce a coexisting A and CNAME

When a load balancer status entry exposes both an IP address and a hostname for the same name, the source layer SHALL select a single target type rather than emitting both an address record and a CNAME for the same DNS name.

#### Scenario: Status entry has both IP and hostname
- **WHEN** a LoadBalancer Service or Ingress status ingress entry populates both `ip` and `hostname`
- **THEN** the controller publishes one target (preferring the hostname) so the resulting name does not have both an A/AAAA record and a CNAME

### Requirement: Hostname-less HTTPRoute is observable

An HTTPRoute that declares no `spec.hostnames` SHALL produce an observable diagnostic rather than silently contributing nothing.

#### Scenario: Route with no hostnames
- **WHEN** an accepted HTTPRoute has an empty `spec.hostnames`
- **THEN** the controller emits an event indicating the route published no hostnames (and that parent listener hostnames are the source of truth) instead of dropping it silently

