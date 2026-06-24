## Why

The first implementation is structurally sound, but review identified several data-safety and operability risks in the diff/apply path of a controller that can mutate real FortiGate DNS records. This change hardens reconciliation, configuration parsing, runtime coordination, and deployment artifacts before the controller is treated as safe for public use.

## What Changes

- Accept the Critical findings around DNS data safety:
  - Replace target-value identity behavior that turns IP changes into create+delete workflows with safe replacement semantics.
  - Make FortiGate apply fail-soft per record, continue independent operations, and return aggregated errors with operation counts.
  - Fail configuration loading on malformed non-empty environment variables instead of silently falling back to defaults.
- Accept the High findings:
  - Add leader election or an equivalent single-writer guard for multi-replica deployments.
  - Remove DNSName fallback for FortiGate record IDs and fail or skip operations when a provider ID is required but unavailable.
- Accept the Medium findings that affect safe operation:
  - Validate FortiGate success envelopes even when HTTP status is 2xx.
  - Add health, readiness, and metrics endpoints.
  - Add per-reconcile and per-request timeout behavior that respects context cancellation.
  - Decouple Gateway target lookup namespaces from ownership cleanup scope so shared infrastructure Gateways can be resolved safely.
  - Make Service publishing behavior explicit for unsupported service types instead of silently ignoring them.
  - Align raw manifests, Helm chart, README, and validation docs.
  - Add restricted Pod Security Standard hardening (seccompProfile RuntimeDefault, readOnlyRootFilesystem, resource requests/limits) and image pinning to the Helm chart and raw manifests.
- Accept low-risk polish that reduces future bugs:
  - Make retry backoff context-aware.
  - Avoid mutating caller-owned target slices during normalization.
  - Make FortiGate owner/source comment parsing more robust.
  - Preserve useful kubeconfig errors.
  - Remove unused helpers or wire them into real behavior.
  - Correct README/container documentation drift.
  - Add tests for DNS keying, config parsing, apply batching, FortiGate envelope handling, health/metrics, Gateway target scope, and manifest drift.
- Deferred or rejected for this change:
  - Do not add GitHub Actions workflows because the repository currently must not enable workflow execution.
  - Do not add broad new DNS providers, service mesh sources, or arbitrary CRD scanning.
  - Do not automatically publish ClusterIP, headless, or NodePort Services unless an explicit supported policy is designed in this change; at minimum, warn and document unsupported defaults.

## Capabilities

### New Capabilities

- `reconciliation-data-safety`: Hardens DNS plan identity, FortiGate apply semantics, provider ID handling, FortiGate response validation, and configuration parsing to prevent unsafe or surprising writes.
- `controller-operability`: Adds single-writer coordination, health/readiness/metrics endpoints, timeout behavior, context-aware retry, and observable operation results.
- `source-publishing-scope`: Clarifies and hardens source publishing semantics for Services and Gateway API, including target lookup scope, unsupported service type reporting, and ownership cleanup boundaries.
- `deployment-artifact-consistency`: Keeps Helm, raw manifests, README, validation documentation, RBAC, and container documentation consistent without adding GitHub workflow files.

### Modified Capabilities

- None.

## Impact

- `internal/dns` endpoint keying and normalization behavior.
- `internal/plan` diffing, replacement ordering, conflict handling, and cleanup rules.
- `internal/fortigate` apply batching, response parsing, provider ID requirements, retry behavior, and error aggregation.
- `internal/config` environment parsing and validation behavior.
- `internal/controller` reconcile timeout, leader election or single-writer guard, health/readiness/metrics serving, and logging.
- `internal/source` Service and Gateway API publishing decisions.
- Helm chart values/templates, raw manifests, README, validation docs, and tests.
- No `.github/workflows` files should be added by this change.
