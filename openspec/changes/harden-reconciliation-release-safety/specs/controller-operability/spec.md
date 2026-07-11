## ADDED Requirements

### Requirement: Long-running startup retry
The long-running controller SHALL continue retrying after an initial reconcile failure, while one-shot mode MUST return that failure to its caller.

#### Scenario: Initial transient failure
- **WHEN** the first long-running reconcile attempt fails and the context remains active
- **THEN** the controller logs the error and performs another attempt after the configured interval

#### Scenario: One-shot failure
- **WHEN** `--once` reconciliation fails
- **THEN** the process exits unsuccessfully with the failure

### Requirement: Credential-free FortiGate URL
Configuration MUST reject a FortiGate base URL containing URL userinfo, query parameters, or a fragment, MUST NOT render an environment-derived URL as a help default, and redacted configuration output MUST NOT expose any such values even for an unvalidated URL.

#### Scenario: URL contains username and password
- **WHEN** the FortiGate URL is `https://user:password@fortigate.example`
- **THEN** startup validation fails without echoing the credential-bearing URL

#### Scenario: Defensive redaction
- **WHEN** redacted configuration is requested for a value containing URL userinfo
- **THEN** the resulting base URL contains neither username nor password

#### Scenario: Query credential and fragment
- **WHEN** the FortiGate URL contains a query parameter or fragment
- **THEN** validation fails with a fixed message and help or redacted output contains neither value

### Requirement: Help is configuration-independent
The command SHALL print help and exit successfully without allowing malformed environment values to preempt the help request.

#### Scenario: Help with malformed environment
- **WHEN** `--help` is invoked while a typed environment variable is malformed
- **THEN** usage is printed, no configuration-error log is emitted, and the process exits zero
