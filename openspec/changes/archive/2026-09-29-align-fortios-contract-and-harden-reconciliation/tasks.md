## 1. FortiGate provider contract

- [x] 1.1 Write zone-relative hostnames and dotted absolute CNAME targets, map listed rows to served names, and reject apex, out-of-zone, and unsupported-type writes without a request
- [x] 1.2 Paginate on `size` and per-page `matched_count`, advance by returned rows, and keep every fail-closed snapshot check
- [x] 1.3 Validate numeric provider IDs at list and apply time, escape the zone path segment once, fail unknown operation types, and bound response bodies
- [x] 1.4 Replace linear retries with jittered exponential backoff that honors `Retry-After`, keep creates non-retried, and close idle connections on client rotation
- [x] 1.5 Pin the snapshot revision digest across the rewrite and verify hostname, CNAME, and pagination behavior against a FortiOS v7.2.11 device

## 2. Reconciliation core

- [x] 2.1 Adopt only A, AAAA, and CNAME rows in exclusive mode and treat multiple CNAME targets per name as a conflict
- [x] 2.2 Skip plan persistence, approval, and revalidation for non-actionable cycles while still passing conflicts to the provider, and stale earlier non-terminal plans
- [x] 2.3 Write terminal plan phases with a detached bounded context after a reconcile timeout and log phase-write failures
- [x] 2.4 Suppress cleanup and record an incomplete policy precondition while any policy is invalid
- [x] 2.5 Keep workqueue backoff under event churn and after exhaustion, deduplicate identical status history, and avoid double-counting the revalidation pass

## 3. Sources and policy

- [x] 3.1 Mark Gateway discovery incomplete for stale HTTPRoute status
- [x] 3.2 Intersect HTTPRoute hostnames per parent attachment, pair names with their parents' addresses, and stage route endpoints against an atomic budget
- [x] 3.3 Normalize hostnames with IDNA, validate DNS-1123 names and load-balancer hostname targets, and skip the zone apex
- [x] 3.4 Accept whole-second duration TTLs and reject unmatchable domain filters
- [x] 3.5 Deny targets outside a single restricted kind and scope compile-invalid policies to their namespace

## 4. Targets, ownership, and runtime

- [x] 4.1 Validate targets individually, exclude invalid and overlapping targets with fixed reasons, and retry transient target list failures
- [x] 4.2 Trim Secret tokens, keep credential sub-reasons, log sanitized client errors, and write status when client construction fails
- [x] 4.3 Use `spec.timeout` per request and the global reconcile timeout per reconciliation, and require in-zone target domain filters
- [x] 4.4 Count every target audit toward liveness, tick only when idle, and derive the automatic window from the resync period
- [x] 4.5 Record target-mode reconcile metrics, log status write failures, and derive status conditions from the audit
- [x] 4.6 Release the leader Lease only after work drains, exit non-zero on leadership loss, and route client-go logs through slog
- [x] 4.7 Release claims after verified deletes, recover orphaned and empty-phase claims, and converge interrupted rebinds under strict evidence

## 5. Deployment, CI, and documentation

- [x] 5.1 Fail multi-replica renders without leader election, render one-shot runs as a Job, and gate the PodDisruptionBudget
- [x] 5.2 Trim platform RBAC to used verbs, grant status RBAC in target mode, and key EndpointSlice RBAC on the global headless flag
- [x] 5.3 Add egress CIDR lists and DNS selectors, a ServiceMonitor, aggregated and absence alerts, and scheduling knobs
- [x] 5.4 Validate policy CIDRs and default target retries in both CRD copies, and document the CRD upgrade step
- [x] 5.5 Add staticcheck, concurrency, and timeouts to CI, pin the Go toolchain and tool versions, guard release versions, and group Kubernetes updates
- [x] 5.6 Add a security policy and update both READMEs, the chart README, and validation results for the new behavior
