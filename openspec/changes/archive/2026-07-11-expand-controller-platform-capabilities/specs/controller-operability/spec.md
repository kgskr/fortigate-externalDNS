## ADDED Requirements

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
Target processing SHALL use configurable minimum debounce and capped exponential retry with jitter. Successful reconciliation SHALL forget retry history, and retry exhaustion SHALL leave the target observable and eligible for periodic audits.

#### Scenario: Event storm coalesces
- **WHEN** many updates for one target arrive within the debounce window
- **THEN** they result in one pending target reconciliation rather than one provider scan per event

### Requirement: Leadership loss stops mutation
When leadership is lost or shutdown begins, workers SHALL stop accepting new mutation work, cancel in-flight reconciliations, and leave queued keys for a future leader without draining them through provider writes.

#### Scenario: Leadership is lost during apply
- **WHEN** the reconcile context is canceled between operations
- **THEN** remaining operations are not sent and status records an interrupted outcome
