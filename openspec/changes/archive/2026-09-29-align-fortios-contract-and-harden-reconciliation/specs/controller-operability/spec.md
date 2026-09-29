## MODIFIED Requirements

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

### Requirement: Long-running startup retry

The long-running controller SHALL continue retrying after an initial reconcile failure, including a failure to list targets in target mode, while one-shot mode MUST return that failure to its caller.

#### Scenario: Initial transient failure
- **WHEN** the first long-running reconcile attempt fails and the context remains active
- **THEN** the controller logs the error and performs another attempt after the configured interval

#### Scenario: One-shot failure
- **WHEN** `--once` reconciliation fails
- **THEN** the process exits unsuccessfully with the failure

### Requirement: Bounded retry and debounce
Target processing SHALL use configurable minimum debounce and capped exponential retry with jitter. Successful reconciliation SHALL forget retry history, and retry exhaustion SHALL leave the target observable and eligible for periodic audits. An event for a key in retry backoff MUST NOT shorten the remaining backoff, and after exhaustion the key SHALL be held at the maximum backoff until a reconciliation succeeds.

#### Scenario: Event storm coalesces
- **WHEN** many updates for one target arrive within the debounce window
- **THEN** they result in one pending target reconciliation rather than one provider scan per event

#### Scenario: Events arrive during backoff
- **WHEN** a failing target keeps receiving source events while it waits in retry backoff
- **THEN** it is reconciled no earlier than the later of its debounce deadline and its remaining backoff
