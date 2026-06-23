## ADDED Requirements

### Requirement: Supported Kubernetes sources

The controller SHALL discover DNS intent only from Kubernetes core `Service`, Kubernetes core `Ingress`, and Kubernetes SIG Gateway API resources.

#### Scenario: Service source enabled
- **WHEN** a supported `Service` contains a configured DNS hostname
- **THEN** the controller produces desired DNS record intent for that hostname using the service target information

#### Scenario: Ingress source enabled
- **WHEN** a supported `Ingress` contains rule or annotation hostnames
- **THEN** the controller produces desired DNS record intent for those hostnames using the ingress target information

#### Scenario: Gateway API source enabled
- **WHEN** a supported Gateway API resource contains hostnames and publishable status addresses
- **THEN** the controller produces desired DNS record intent for those hostnames using the Gateway API target information

### Requirement: Unsupported sources excluded

The controller MUST NOT watch or reconcile service mesh resources or unrelated third-party CRDs.

#### Scenario: Service mesh resource exists
- **WHEN** an Istio, Linkerd, Consul, Kuma, or similar service mesh routing resource exists in the cluster
- **THEN** the controller ignores that resource and does not create DNS intent from it

#### Scenario: Arbitrary CRD contains hostname
- **WHEN** an unrelated third-party CRD contains a field that looks like a hostname
- **THEN** the controller ignores that CRD and does not create DNS intent from it

### Requirement: Explicit hostname selection

The controller SHALL derive hostnames from standard supported resource fields and documented annotations only.

#### Scenario: Hostname annotation present
- **WHEN** a supported resource has the documented hostname annotation
- **THEN** the controller uses that annotation as an explicit DNS hostname source

#### Scenario: No supported hostname source
- **WHEN** a supported resource has no supported hostname field or annotation
- **THEN** the controller does not create DNS record intent for that resource

### Requirement: Target selection

The controller SHALL derive DNS record targets from supported resource status or spec fields that represent publishable addresses.

#### Scenario: Publishable address exists
- **WHEN** a supported resource reports one or more publishable IP addresses or DNS names
- **THEN** the controller includes those targets in the desired DNS record intent

#### Scenario: Publishable address missing
- **WHEN** a supported resource has a hostname but no publishable target
- **THEN** the controller records no writeable DNS change for that resource and reports the missing target condition in logs or status output

### Requirement: Resource filtering

The controller SHALL support namespace, domain, and source selection filters.

#### Scenario: Namespace excluded
- **WHEN** a supported resource is outside the configured namespace scope
- **THEN** the controller ignores that resource

#### Scenario: Domain excluded
- **WHEN** a discovered hostname does not match the configured domain filter
- **THEN** the controller does not create DNS intent for that hostname

#### Scenario: Source disabled
- **WHEN** a source type is disabled in configuration
- **THEN** the controller does not watch or process that source type
