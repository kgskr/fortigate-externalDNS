## ADDED Requirements

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
