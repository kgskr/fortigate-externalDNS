## ADDED Requirements

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
