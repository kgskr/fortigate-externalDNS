## Why

The controller can currently delete healthy DNS records after dependent create or partial source-discovery failures, can reconcile from incomplete FortiGate list results, and relies on an ownership field that is not present in the documented FortiOS `dns-entry` schema. The deployment artifacts also contain fail-open egress, stale CA rollout, validation, and toolchain gaps that block a safe release.

## What Changes

- Replace undocumented per-record comment ownership with a supported, explicit ownership contract that fails closed when ownership cannot be proven.
- Make replacement operations dependency-aware so failed creates cannot be followed by destructive cleanup, and make unowned logical siblings block all mutation of that DNS name and type.
- Detect incomplete FortiGate list responses and fetch every result page before planning.
- Treat unavailable configured Kubernetes source APIs as incomplete discovery, validate Gateway address types, and retry transient startup reconciliation failures in long-running mode.
- Reject credential-bearing FortiGate URLs, roll pods when the configured CA changes, and tighten Helm egress and duration validation.
- Upgrade the Go patch toolchain, strengthen committed-secret scanning, repair OpenSpec validation, and make shell documentation examples portable to zsh.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `reconciliation-data-safety`: Require supported ownership persistence, complete FortiGate pagination, dependency-aware apply, and logical-sibling conflict safety.
- `source-publishing-scope`: Require configured source availability and typed, DNS-valid Gateway targets.
- `controller-operability`: Retry transient reconcile failures in long-running mode and reject credential-bearing base URLs.
- `deployment-artifact-consistency`: Tighten egress peers, duration schemas, CA rollout, secret scanning, documentation commands, and OpenSpec validation.
- `supply-chain-security`: Move all Go build inputs to the patched toolchain version that passes the vulnerability gate.

## Impact

Affected areas include `internal/fortigate`, `internal/plan`, `internal/source`, `internal/controller`, configuration parsing, the Helm chart, validation scripts, CI/build inputs, user documentation, and baseline OpenSpec specifications. FortiGate installations that depended on the undocumented `comment` field must migrate to the supported ownership mode selected by this change before enabling writes.
