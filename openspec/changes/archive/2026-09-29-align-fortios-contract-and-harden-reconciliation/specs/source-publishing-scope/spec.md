## ADDED Requirements

### Requirement: HTTPRoute hostnames follow listener attachment
HTTPRoute publication SHALL intersect route hostnames with the hostnames of the listeners each accepted parent reference attaches to, honoring `sectionName` and `port`. A listener without a hostname SHALL match any route hostname, and a `*.` listener SHALL match subdomains but not its apex. Each resulting DNS name MUST be paired only with the addresses of the parents that serve it, and the endpoint budget SHALL apply to the route as one resource.

#### Scenario: Route hostname outside the attached listeners
- **WHEN** a route declares `a.example.com` and `login.example.com` but its accepted parent listener only serves `a.example.com`
- **THEN** only `a.example.com` is published and `login.example.com` is skipped with a warning

#### Scenario: Parents serve different hostnames
- **WHEN** one accepted Gateway serves `a.example.com` at one address and another serves `b.example.com` at another address for the same route
- **THEN** each DNS name is published only with the address of the Gateway that serves it

#### Scenario: Oversized multi-parent route
- **WHEN** the combined endpoints of all parent attachments of one route exceed the per-resource or remaining discovery budget
- **THEN** the whole route is rejected, Gateway discovery is marked incomplete, and the rejected route consumes none of the budget available to other sources

### Requirement: Hostnames are normalized and validated
Source hostnames SHALL be normalized to lowercase ASCII using IDNA lookup rules and MUST be DNS-1123 subdomains of at most 253 characters, allowing only a leading `*.` label before wildcard rejection. Invalid hostnames and invalid load-balancer hostname targets SHALL be skipped with a warning without marking discovery incomplete.

#### Scenario: Internationalized hostname
- **WHEN** an annotation declares a Unicode hostname inside the zone
- **THEN** the controller publishes its punycode form

#### Scenario: Malformed hostname
- **WHEN** an annotation declares a name with spaces, underscores, empty labels, or more than 253 characters
- **THEN** the name is skipped with a warning and does not fail every reconcile at the provider

#### Scenario: Invalid load-balancer hostname target
- **WHEN** a Service or Ingress load-balancer status reports a hostname that is not a valid DNS name
- **THEN** that target is skipped with a warning

### Requirement: TTL annotation formats
The TTL annotation SHALL accept integer seconds or a whole-second Go duration such as `5m`, within 1 to the maximum TTL, and MUST reject sub-second or non-integral-second durations.

#### Scenario: Duration TTL
- **WHEN** a source sets the TTL annotation to `5m`
- **THEN** its records use a TTL of 300 seconds

#### Scenario: Sub-second TTL
- **WHEN** a source sets the TTL annotation to `1500ms` or `0s`
- **THEN** the annotation is rejected as invalid

### Requirement: Domain filters are exact DNS suffixes
Configuration MUST reject a domain filter that starts with `.` or contains `*`, because such a filter can never match a normalized hostname.

#### Scenario: Unmatchable domain filter
- **WHEN** `--domain-filter=.example.com` or `--domain-filter=*.example.com` is configured
- **THEN** startup validation fails with an error naming the filter instead of silently publishing nothing

## MODIFIED Requirements

### Requirement: Accepted Gateway API parent matching

HTTPRoute publishing SHALL use targets only from Gateway parent references whose full parent identity is accepted and has resolved references for the route's current generation. A route whose status carries no `Accepted` or `ResolvedRefs` condition for its current generation MUST mark Gateway discovery incomplete so cleanup waits for current status.

#### Scenario: Mixed accepted and rejected parents
- **WHEN** a route has one accepted Gateway parent and one rejected Gateway parent
- **THEN** only the accepted Gateway target contributes to desired DNS records

#### Scenario: Accepted non-Gateway parent
- **WHEN** a route status marks a non-Gateway parent as accepted
- **THEN** that status does not authorize publishing targets from a Gateway with the same name

#### Scenario: Stale accepted status
- **WHEN** an HTTPRoute has a newer generation than its `Accepted=True` and `ResolvedRefs=True` parent conditions observed
- **THEN** the controller treats the parent as not currently accepted, does not publish the route hostname from that stale status, and marks Gateway discovery incomplete so existing records are not cleaned up

#### Scenario: Current rejection is not incomplete
- **WHEN** an HTTPRoute's current-generation status reports `Accepted=False` or `ResolvedRefs=False`
- **THEN** the route is not published and Gateway discovery remains complete

### Requirement: Published hostnames must be within the configured zone

The source layer SHALL only publish a desired record when the hostname is a subdomain of the configured FortiGate zone. A hostname outside the zone MUST NOT be written into the zone's `dns-database`, and a hostname equal to the zone apex SHALL be skipped with a warning because FortiGate `dns-entry` names are zone-relative.

#### Scenario: Out-of-zone hostname
- **WHEN** a Service, Ingress, Gateway, or HTTPRoute declares a hostname that is neither the configured zone nor a subdomain of it
- **THEN** the controller does not create a record for that hostname and emits a warning event explaining it is outside the configured zone

#### Scenario: In-zone hostname
- **WHEN** a resource declares a hostname that is a subdomain of the configured zone
- **THEN** the controller publishes the corresponding desired record as before

#### Scenario: Zone apex hostname
- **WHEN** a resource declares a hostname equal to the configured zone
- **THEN** the controller skips it with a warning event instead of failing the provider write every cycle

### Requirement: Typed Gateway addresses

Gateway and HTTPRoute publishing SHALL accept only Gateway status addresses whose type and value form a valid IP address or hostname, SHALL ignore custom address types, and MUST select hostname targets over IP targets among the accepted parents that serve the same published DNS name.

#### Scenario: IP and hostname addresses coexist
- **WHEN** accepted Gateway parents serving the same DNS name expose both valid IPAddress and Hostname values
- **THEN** that DNS name receives only CNAME targets

#### Scenario: Invalid or custom typed value
- **WHEN** an address type and value disagree or the address uses a custom type
- **THEN** the value is not published and an observable diagnostic is emitted
