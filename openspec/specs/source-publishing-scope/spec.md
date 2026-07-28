# source-publishing-scope Specification

## Purpose
Defines which Kubernetes resources and hostnames become DNS records and bounds that
scope: only in-zone, non-wildcard hostnames are published; a name never gets both an
address and a CNAME; Gateway target-namespace lookup does not widen ownership or
cleanup; Gateway listener hostnames remain published when HTTPRoutes are empty;
HTTPRoute targets come only from current accepted, resolved Gateway parents; and
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

HTTPRoute publishing SHALL use targets only from Gateway parent references whose full parent identity is accepted and has resolved references for the route's current generation.

#### Scenario: Mixed accepted and rejected parents
- **WHEN** a route has one accepted Gateway parent and one rejected Gateway parent
- **THEN** only the accepted Gateway target contributes to desired DNS records

#### Scenario: Accepted non-Gateway parent
- **WHEN** a route status marks a non-Gateway parent as accepted
- **THEN** that status does not authorize publishing targets from a Gateway with the same name

#### Scenario: Stale accepted status
- **WHEN** an HTTPRoute has a newer generation than its `Accepted=True` and `ResolvedRefs=True` parent conditions observed
- **THEN** the controller treats the parent as not currently accepted and does not publish the route hostname from that stale status

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

### Requirement: Gateway source remains active when HTTPRoutes are empty

Gateway API discovery SHALL distinguish an available resource with zero objects from an unavailable configured source, SHALL continue publishing Gateway listeners when HTTPRoutes are empty, and MUST mark discovery incomplete when required Gateway API resources are missing or gone so cleanup is suppressed.

#### Scenario: Gateway exists with zero HTTPRoutes
- **WHEN** the Gateway source is enabled, Gateway API resources are available, a Gateway has listener hostnames, and there are no HTTPRoutes
- **THEN** the controller still publishes desired records for the Gateway listener hostnames

#### Scenario: HTTPRoute resource unavailable
- **WHEN** the HTTPRoute resource itself is unavailable because Gateway API CRDs are not installed
- **THEN** Service and Ingress desired records may continue but Gateway discovery is marked incomplete and no cleanup is applied that cycle

#### Scenario: Gateway resource unavailable
- **WHEN** HTTPRoutes are available but the configured Gateway resource cannot be listed
- **THEN** discovery is marked incomplete and no cleanup is applied that cycle

### Requirement: Typed Gateway addresses

Gateway and HTTPRoute publishing SHALL accept only Gateway status addresses whose type and value form a valid IP address or hostname, SHALL ignore custom address types, and MUST select hostname targets over IP targets across all accepted parents.

#### Scenario: IP and hostname addresses coexist
- **WHEN** accepted Gateway parents expose both valid IPAddress and Hostname values
- **THEN** the published DNS name receives only CNAME targets

#### Scenario: Invalid or custom typed value
- **WHEN** an address type and value disagree or the address uses a custom type
- **THEN** the value is not published and an observable diagnostic is emitted

### Requirement: Opt-in ExternalName publication
When ExternalName publication is enabled, an annotated `ExternalName` Service SHALL publish each accepted source hostname as a CNAME to its valid non-IP `spec.externalName`, subject to domain, policy, TTL, and CNAME conflict rules.

#### Scenario: Valid ExternalName is published
- **WHEN** an opted-in ExternalName Service has an accepted hostname and a DNS-valid non-IP external name
- **THEN** the desired set contains one normalized CNAME per accepted hostname

#### Scenario: IP or malformed ExternalName is rejected
- **WHEN** `spec.externalName` is an IP literal or invalid DNS hostname
- **THEN** no endpoint is published and a warning reason is emitted

### Requirement: Opt-in headless EndpointSlice publication
When headless publication is enabled and granted by annotation or policy, a headless Service SHALL derive A and AAAA targets from EndpointSlices bearing the standard service-name label. Addresses SHALL be normalized, deduplicated, and filtered by readiness semantics.

#### Scenario: Ready dual-stack endpoints are published
- **WHEN** matching EndpointSlices contain ready IPv4 and IPv6 addresses
- **THEN** the desired set contains deterministic A and AAAA records for the accepted source hostnames

#### Scenario: Unready endpoint is excluded by default
- **WHEN** an endpoint explicitly reports ready false and the Service does not publish not-ready addresses
- **THEN** its addresses are excluded

#### Scenario: Publish-not-ready is intentional
- **WHEN** `publishNotReadyAddresses` is true and policy permits headless publication
- **THEN** valid addresses from matching endpoints may be included regardless of ready false

#### Scenario: Malformed headless opt-in is object-local
- **WHEN** one headless Service has a malformed publication annotation while Service and EndpointSlice discovery otherwise complete successfully
- **THEN** that Service is rejected with a bounded diagnostic while unrelated stale-record cleanup remains eligible under the normal safety guards

### Requirement: EndpointSlice discovery participates in cleanup safety
Failure to list required EndpointSlices or an unsynchronized EndpointSlice informer SHALL mark Service discovery incomplete and suppress cleanup for affected targets.

#### Scenario: EndpointSlice API list fails
- **WHEN** headless publication is enabled and EndpointSlice discovery fails
- **THEN** no stale headless records are cleaned during that reconciliation

### Requirement: Unsupported source modes remain explicit
NodePort target derivation, ordinary ClusterIP publication, wildcard hostnames, SRV records, arbitrary CRDs, and service-mesh sources SHALL remain unsupported and SHALL produce bounded diagnostics when explicitly requested.

#### Scenario: NodePort Service requests publication
- **WHEN** a NodePort Service has a hostname annotation but no supported external target
- **THEN** it publishes no endpoint and reports the unsupported mode
