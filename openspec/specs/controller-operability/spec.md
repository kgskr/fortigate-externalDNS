# controller-operability Specification

## Purpose
Guarantees the controller runs safely and observably in a cluster: a single writer
reconciles at a time (leader election), each reconcile loop is time-bounded and
cancellable, FortiGate retries respect context cancellation, FortiGate TLS trust is
configurable without disabling verification, health, readiness, and secret-free
metrics endpoints are exposed for Kubernetes probes and scraping, liveness reflects
reconcile-loop progress, and log shape and build identity are operator-configurable.
## Requirements
### Requirement: Single-writer reconciliation

The controller SHALL support Kubernetes Lease-based leader election or an equivalent single-writer guard for in-cluster deployments. The Lease MUST NOT be released while this replica may still be writing: shutdown SHALL cancel reconcile work, wait for it to return, and only then release the Lease, and losing leadership outside shutdown SHALL end the process unsuccessfully.

#### Scenario: Multiple replicas running
- **WHEN** two controller pods are deployed with leader election enabled
- **THEN** only the elected leader performs FortiGate reconciliation

#### Scenario: Non-leader pod healthy
- **WHEN** a pod is not the current leader
- **THEN** it remains live but does not apply DNS changes

#### Scenario: Graceful shutdown drains before releasing
- **WHEN** the leader receives a termination signal during reconciliation
- **THEN** the reconcile work is cancelled and has returned before the Lease is released to another replica

#### Scenario: Leadership lost outside shutdown
- **WHEN** the leader fails to renew its Lease while the process is not shutting down
- **THEN** reconciliation stops and the process exits with a non-zero status instead of reporting a clean stop

### Requirement: Reconcile timeout

Each reconciliation loop SHALL run with a configured timeout that bounds Kubernetes list calls and FortiGate API calls.

#### Scenario: Kubernetes list hangs
- **WHEN** a Kubernetes API call exceeds the reconcile timeout
- **THEN** the reconcile loop is canceled and a later loop can retry

### Requirement: Context-aware FortiGate retry

FortiGate retry backoff MUST respect context cancellation, SHALL grow exponentially with full jitter up to a bounded maximum, and SHALL honor a bounded `Retry-After` value on HTTP 429 or 503 for requests that are safe to retry.

#### Scenario: Context canceled during retry sleep
- **WHEN** the reconciliation context is canceled while waiting between retries
- **THEN** retry sleep exits promptly and the operation returns the context error

#### Scenario: Rate limited with Retry-After
- **WHEN** a keyed FortiGate request returns HTTP 429 or 503 with `Retry-After`
- **THEN** the next attempt waits at least that long, capped and bounded by the request context, while create requests remain non-retried

### Requirement: Health and readiness endpoints

The controller SHALL expose health and readiness endpoints for Kubernetes probes; while a replica is responsible for reconciling, `/healthz` MUST return non-success when no reconcile attempt has completed within the configurable staleness window, replicas not responsible for reconciling MUST remain live, and completed attempts with errors SHALL count as heartbeat progress. In target mode the automatic window SHALL derive from the larger of the interval and resync period, every target audit attempt SHALL count as progress, and a periodic tick SHALL count as progress only when no target audit can run.

#### Scenario: Process running and reconciling
- **WHEN** the controller process is running, its HTTP probe server is available, and reconcile attempts are completing within the staleness window
- **THEN** `/healthz` returns success

#### Scenario: Leader loop is wedged
- **WHEN** the replica holds leadership but no reconcile attempt has completed within the staleness window
- **THEN** `/healthz` returns a non-success status so the kubelet restarts the pod

#### Scenario: FortiGate outage does not fail liveness
- **WHEN** reconcile attempts are completing on schedule but failing because the FortiGate device is unreachable
- **THEN** `/healthz` continues to return success, and the failure remains observable through error metrics and the last-successful-reconcile timestamp

#### Scenario: Non-leader pod stays live
- **WHEN** a replica does not hold leadership and therefore performs no reconcile attempts
- **THEN** `/healthz` returns success for that replica

#### Scenario: Controller not ready
- **WHEN** required clients or configuration are not ready
- **THEN** `/readyz` returns a non-success status

#### Scenario: Target mode has no targets
- **WHEN** event-driven target mode runs with zero targets or cannot list targets
- **THEN** each resync tick counts as progress and `/healthz` keeps returning success

#### Scenario: Event worker is wedged
- **WHEN** targets exist but no target audit completes within the staleness window
- **THEN** resync ticks do not count as progress and `/healthz` returns a non-success status

#### Scenario: Long resync period
- **WHEN** target mode uses `--resync=10m` with the automatic staleness window
- **THEN** the window is at least five resync periods so healthy pods are not restarted between audits

### Requirement: Metrics endpoint

The controller SHALL expose operational metrics without leaking secrets.

#### Scenario: Metrics scraped
- **WHEN** `/metrics` is requested
- **THEN** the response includes reconcile counts, duration, operation counts, and error counts without FortiGate tokens or secret values

### Requirement: Startup validation of bind and leader-election configuration

Configuration validation SHALL reject a malformed metrics bind address and a missing leader-election identity at startup rather than failing later in a background goroutine or inside the leader-election machinery.

#### Scenario: Malformed metrics address
- **WHEN** a non-empty `metrics-addr` cannot be parsed as a host:port bind address
- **THEN** configuration validation fails at startup with a clear error naming the value

#### Scenario: Empty leader-election ID with leader election enabled
- **WHEN** leader election is enabled (and the controller is not running with `--once`) but the leader-election ID is empty or whitespace
- **THEN** configuration validation fails at startup with a clear error

### Requirement: Probe and metrics server bind failure is fatal

When a metrics/probe HTTP server is configured, a failure to bind its listener MUST cause the controller to fail loudly rather than continue reconciling while reporting itself ready with no reachable health endpoint.

#### Scenario: Metrics address already in use
- **WHEN** the configured metrics/probe address cannot be bound (for example the port is already in use)
- **THEN** the controller surfaces the bind failure as a fatal startup error and does not report readiness as true with no listener bound

#### Scenario: Shutdown flips readiness
- **WHEN** the controller receives a termination signal
- **THEN** it reports not-ready before tearing down so traffic and probes observe the draining state

### Requirement: Apply-outcome metrics

Operation metrics SHALL distinguish planned operations from applied outcomes so operators can alert on apply failures.

#### Scenario: Apply results recorded
- **WHEN** a reconcile batch applies operations against FortiGate
- **THEN** the operations metric records outcome results (such as applied, failed, skipped, and conflict) in addition to planned, and the metric's documentation matches what is recorded

#### Scenario: Bounded numeric configuration
- **WHEN** `default-ttl` or FortiGate `retries` is set to an obviously out-of-range value
- **THEN** configuration validation rejects it at startup rather than forwarding it to the device or multiplying request latency

### Requirement: Sensitive configuration values are not exposed in help

Configuration loading SHALL support FortiGate API tokens from environment variables and explicit flags without surfacing secret values in generated CLI help or default text.

#### Scenario: Token provided by environment
- **WHEN** `FORTIGATE_API_TOKEN` is set and help output is rendered
- **THEN** the help output does not include the token value while runtime configuration still receives the token

#### Scenario: Token flag overrides environment
- **WHEN** both `FORTIGATE_API_TOKEN` and `--fortigate-api-token` are supplied
- **THEN** the explicit flag value wins without making either secret appear as a help default

### Requirement: FortiGate TLS trust is configurable and fails closed

The controller SHALL accept a PEM CA bundle path (`--fortigate-ca-file` / `FORTIGATE_CA_FILE`) as the FortiGate trust root set, SHALL enforce TLS 1.2 or newer, and MUST reject a CA file combined with insecure verification or a CA file that is unreadable or contains no PEM certificate.

#### Scenario: Private-CA device verified
- **WHEN** a CA file containing the device's issuing CA chain is configured and the FortiGate presents a certificate signed by that chain
- **THEN** the TLS connection verifies successfully without `insecure-skip-verify`

#### Scenario: Contradictory trust configuration
- **WHEN** both a CA file and `insecure-skip-verify` are configured
- **THEN** configuration validation fails at startup with an error naming both options

#### Scenario: Malformed CA file
- **WHEN** the configured CA file is missing, unreadable, or contains no PEM certificate
- **THEN** the controller fails at startup with a clear error instead of silently falling back to system roots

#### Scenario: Legacy TLS rejected
- **WHEN** the device offers only TLS 1.1 or lower
- **THEN** the client refuses the connection

### Requirement: Structured logging configuration

The controller SHALL support `--log-format` (`text` or `json`) and `--log-level` (`debug`, `info`, `warn`, `error`) flags with environment equivalents, MUST reject invalid values rather than silently defaulting, and SHALL route client-go log output through the same structured logger.

#### Scenario: JSON logs for aggregation
- **WHEN** `--log-format=json` is set
- **THEN** log output is line-delimited JSON produced by the structured logger

#### Scenario: Invalid log configuration
- **WHEN** `LOG_FORMAT=xml` or `--log-level=verbose` is supplied
- **THEN** startup fails with a clear error naming the invalid value

#### Scenario: Client-go logs follow the format
- **WHEN** leader election or informers log through client-go while `--log-format=json` is set
- **THEN** those lines are also emitted as structured JSON

### Requirement: Version identity is reported

The build SHALL stamp a version and commit into the binary, `--version` SHALL print them and exit successfully without further configuration, and the metrics endpoint SHALL expose a `build_info` gauge labeled with both values.

#### Scenario: Version flag
- **WHEN** `fortigate-external-dns --version` is invoked with no other configuration
- **THEN** it prints the stamped version and commit and exits 0

#### Scenario: Running pod correlated to code
- **WHEN** `/metrics` is scraped
- **THEN** the response includes a `build_info` gauge with value 1 carrying version and commit labels

#### Scenario: Release image is stamped
- **WHEN** the release workflow builds the container image for a version tag
- **THEN** the embedded version matches the release tag rather than a development placeholder

### Requirement: Long-running startup retry

The long-running controller SHALL continue retrying after an initial reconcile failure, including a failure to list targets in target mode, while one-shot mode MUST return that failure to its caller.

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

### Requirement: Event-driven target workqueue
The controller SHALL watch enabled source resources, EndpointSlices, targets, policies, ownership claims, change plans, and referenced Secret metadata and SHALL map relevant events to rate-limited target keys. Duplicate pending keys SHALL coalesce and only one reconcile per target SHALL execute at a time.

#### Scenario: Source object changes
- **WHEN** an enabled source object is added, updated meaningfully, or deleted
- **THEN** every affected target key is enqueued without waiting for the periodic interval

#### Scenario: Status-only update is irrelevant
- **WHEN** an update does not change any field used for discovery, policy, target configuration, ownership, or approval
- **THEN** the handler does not create an unnecessary queue item

### Requirement: Periodic full audit remains authoritative
The controller SHALL periodically enqueue every configured target even when no Kubernetes event occurs. Each reconcile SHALL build desired state from informer caches and obtain a stable complete provider snapshot before allowing cleanup.

#### Scenario: External provider drift occurs
- **WHEN** a FortiGate record changes outside Kubernetes and no source event occurs
- **THEN** the next periodic audit detects and reports the drift

### Requirement: Bounded retry and debounce
Target processing SHALL use configurable minimum debounce and capped exponential retry with jitter. Successful reconciliation SHALL forget retry history, and retry exhaustion SHALL leave the target observable and eligible for periodic audits. An event for a key in retry backoff MUST NOT shorten the remaining backoff, and after exhaustion the key SHALL be held at the maximum backoff until a reconciliation succeeds.

#### Scenario: Event storm coalesces
- **WHEN** many updates for one target arrive within the debounce window
- **THEN** they result in one pending target reconciliation rather than one provider scan per event

#### Scenario: Events arrive during backoff
- **WHEN** a failing target keeps receiving source events while it waits in retry backoff
- **THEN** it is reconciled no earlier than the later of its debounce deadline and its remaining backoff

### Requirement: Leadership loss stops mutation
When leadership is lost or shutdown begins, workers SHALL stop accepting new mutation work, cancel in-flight reconciliations, and leave queued keys for a future leader without draining them through provider writes.

#### Scenario: Leadership is lost during apply
- **WHEN** the reconcile context is canceled between operations
- **THEN** remaining operations are not sent and status records an interrupted outcome

