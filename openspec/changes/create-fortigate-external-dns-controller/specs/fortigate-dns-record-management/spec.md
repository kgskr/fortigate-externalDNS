## ADDED Requirements

### Requirement: FortiGate-only DNS backend

The controller SHALL apply DNS changes only through FortiGate API calls.

#### Scenario: DNS apply is requested
- **WHEN** the controller has a DNS change plan to apply
- **THEN** it sends create, update, or delete requests only to the configured FortiGate endpoint

#### Scenario: Non-FortiGate provider requested
- **WHEN** configuration attempts to select a non-FortiGate DNS provider
- **THEN** the controller rejects the configuration before starting reconciliation

### Requirement: FortiGate authentication and connection configuration

The controller MUST accept FortiGate connection settings from Kubernetes-safe configuration sources and MUST NOT require credentials to be stored in source-controlled manifests.

#### Scenario: Token secret configured
- **WHEN** a Kubernetes Secret provides the FortiGate API token or credentials
- **THEN** the controller authenticates to FortiGate without logging the secret value

#### Scenario: Missing credentials
- **WHEN** FortiGate credentials are not configured
- **THEN** the controller fails startup with a clear configuration error

### Requirement: Desired state reconciliation

The controller SHALL reconcile FortiGate DNS records to match desired records derived from supported Kubernetes resources.

#### Scenario: Desired record missing in FortiGate
- **WHEN** a desired managed DNS record does not exist in FortiGate
- **THEN** the controller creates the record through the FortiGate API

#### Scenario: Existing managed record differs
- **WHEN** a FortiGate DNS record owned by the controller has different targets or TTL than desired
- **THEN** the controller updates the record through the FortiGate API

#### Scenario: Managed record no longer desired
- **WHEN** a FortiGate DNS record owned by the controller no longer has a matching desired Kubernetes source
- **THEN** the controller deletes or deactivates that record according to the configured policy

### Requirement: Ownership protection

The controller MUST distinguish records it manages from records it does not manage before updating or deleting FortiGate DNS records.

#### Scenario: Unowned record exists
- **WHEN** FortiGate contains a DNS record for the same hostname that is not owned by this controller
- **THEN** the controller does not update or delete that record and reports an ownership conflict

#### Scenario: Owner ID matches
- **WHEN** FortiGate contains a DNS record with this controller's configured owner ID
- **THEN** the controller is allowed to update or delete that record as required by reconciliation

### Requirement: Dry-run mode

The controller SHALL support a dry-run mode that computes and reports DNS changes without modifying FortiGate.

#### Scenario: Dry-run enabled
- **WHEN** reconciliation finds records to create, update, or delete while dry-run is enabled
- **THEN** the controller reports the planned changes and sends no mutating FortiGate API requests

### Requirement: Error handling and retry

The controller SHALL handle FortiGate API failures without losing the desired state and SHALL retry reconciliation on later loops.

#### Scenario: FortiGate API request fails
- **WHEN** a FortiGate API request fails due to timeout, authentication, validation, or server error
- **THEN** the controller logs the failed operation without secret values and retries according to the configured reconciliation interval or Kubernetes requeue behavior
