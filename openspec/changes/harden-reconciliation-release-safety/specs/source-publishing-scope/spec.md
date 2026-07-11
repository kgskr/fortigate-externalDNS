## ADDED Requirements

### Requirement: Typed Gateway addresses
Gateway and HTTPRoute publishing SHALL accept only Gateway status addresses whose type and value form a valid IP address or hostname, SHALL ignore custom address types, and MUST select hostname targets over IP targets across all accepted parents.

#### Scenario: IP and hostname addresses coexist
- **WHEN** accepted Gateway parents expose both valid IPAddress and Hostname values
- **THEN** the published DNS name receives only CNAME targets

#### Scenario: Invalid typed value
- **WHEN** an IPAddress value is not an IP address or a Hostname value is itself an IP address
- **THEN** the value is not published and an observable diagnostic is emitted

#### Scenario: Custom address type
- **WHEN** a Gateway status address uses a named or implementation-specific address type
- **THEN** the value is not interpreted as a CNAME target

## MODIFIED Requirements

### Requirement: Gateway source remains active when HTTPRoutes are empty
Gateway API discovery SHALL distinguish an available resource with zero objects from an unavailable configured source, SHALL continue publishing Gateway listeners when HTTPRoutes are empty, and MUST mark discovery incomplete when required Gateway API resources are missing or gone so cleanup is suppressed.

#### Scenario: Gateway exists with zero HTTPRoutes
- **WHEN** the Gateway source is enabled, Gateway API resources are available, a Gateway has listener hostnames, and there are no HTTPRoutes
- **THEN** the controller still publishes desired records for the Gateway listener hostnames

#### Scenario: HTTPRoute resource unavailable
- **WHEN** the configured Gateway source cannot list HTTPRoutes because the resource is missing or gone
- **THEN** Service and Ingress desired records may continue but the result marks Gateway discovery incomplete and no cleanup is applied that cycle

#### Scenario: Gateway resource unavailable
- **WHEN** HTTPRoutes are available but the configured Gateway resource cannot be listed
- **THEN** the result marks Gateway discovery incomplete and no cleanup is applied that cycle
