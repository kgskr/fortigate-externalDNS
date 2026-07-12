## 1. FortiGate and planner safety

- [x] 1.1 Remove undocumented record-comment ownership serialization and make exclusive-zone current records owned in the controller
- [x] 1.2 Implement complete, revision-stable FortiGate pagination with fail-closed metadata and provider-ID checks
- [x] 1.3 Make non-1:1 target transitions converge in two phases without same-cycle stale cleanup
- [x] 1.4 Skip dependent cleanup after a failed create while continuing independent logical records
- [x] 1.5 Treat any unowned logical sibling as a conflict before owned-row updates or duplicate cleanup
- [x] 1.6 Add FortiGate and planner regression tests for ownership payloads, pagination, replacement safety, apply dependencies, and conflicts

## 2. Discovery, runtime, and configuration safety

- [x] 2.1 Track incomplete Kubernetes sources and suppress cleanup when configured discovery is incomplete
- [x] 2.2 Validate Gateway address types and enforce hostname-over-IP target exclusivity across accepted parents
- [x] 2.3 Retry an initial long-running reconcile failure while preserving one-shot error behavior
- [x] 2.4 Add exclusive-zone acknowledgement and restricted-cleanup validation to configuration
- [x] 2.5 Reject and defensively redact credential-bearing FortiGate URLs
- [x] 2.6 Make help exit successfully even when typed environment values are malformed
- [x] 2.7 Add source, controller, config, and command regression tests for the changed behavior

## 3. Deployment and delivery hardening

- [x] 3.1 Require explicit DNS, Kubernetes API, and FortiGate peers in the opt-in egress NetworkPolicy and reject empty API ports
- [x] 3.2 Add a FortiGate CA checksum to the Pod template and test that CA rotation changes it
- [x] 3.3 Reject zero runtime durations in the Helm values schema
- [x] 3.4 Detect quoted YAML and JSON token keys in the committed-secret scanner with regression fixtures
- [x] 3.5 Upgrade go.mod and the pinned Containerfile builder to Go 1.26.5 using a verified manifest-list digest
- [x] 3.6 Quote zsh-sensitive Helm and Secret command examples and update ownership/network-policy migration documentation
- [x] 3.7 Extend Helm rendering checks for required peers, duration rejection, checksum rotation, and exclusive-zone arguments

## 4. Specification and validation closure

- [x] 4.1 Update baseline OpenSpec requirements for exclusive-zone ownership, pagination, dependency safety, incomplete discovery, deployment hardening, and Go alignment
- [x] 4.2 Rewrite multiline baseline requirement prose into parser-safe normative lines and make strict OpenSpec validation pass
- [x] 4.3 Add strict OpenSpec validation to the repository validation target and continuous integration
- [x] 4.4 Run formatting, unit, race, static, Helm, secret, OpenSpec, vulnerability, and container-build validation
