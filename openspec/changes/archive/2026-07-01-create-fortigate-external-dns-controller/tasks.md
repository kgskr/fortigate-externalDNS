## 1. Project Foundation

- [x] 1.1 Initialize the Go module, command entrypoint, package layout, and baseline configuration loading.
- [x] 1.2 Define the internal DNS endpoint model with hostname, record type, targets, TTL, zone, source reference, and owner metadata.
- [x] 1.3 Add controller runtime wiring for Kubernetes client setup, reconciliation interval, logging, dry-run, domain filters, namespace filters, and enabled source selection.
- [x] 1.4 Add initial unit test structure and test helpers for source extraction, planning, and FortiGate client behavior.

## 2. Kubernetes Resource Discovery

- [x] 2.1 Implement `Service` source discovery for documented hostname and TTL annotations plus publishable service targets.
- [x] 2.2 Implement `Ingress` source discovery for rule hostnames, documented annotations, TTL, and ingress status targets.
- [x] 2.3 Implement Gateway API source discovery for the initial supported Gateway API resources and publishable status addresses.
- [x] 2.4 Implement namespace, domain, and source-type filters across all supported sources.
- [x] 2.5 Ensure the controller does not watch or process service mesh resources or arbitrary third-party CRDs.
- [x] 2.6 Add unit tests covering supported source extraction, missing target handling, unsupported source exclusion, and filter behavior.

## 3. Planning and Reconciliation

- [x] 3.1 Implement desired/current record comparison that produces create, update, delete, and conflict operations.
- [x] 3.2 Implement dry-run output that reports planned operations without mutating FortiGate.
- [x] 3.3 Implement ownership checks using the selected owner metadata strategy and conflict reporting for unowned records.
- [x] 3.4 Implement stale managed record cleanup according to the configured delete or deactivate policy.
- [x] 3.5 Add unit tests for idempotent planning, ownership conflicts, stale record cleanup, and dry-run behavior.

## 4. FortiGate API Integration

- [x] 4.1 Implement FortiGate configuration validation for endpoint URL, credentials or token, VDOM or DNS context, TLS options, zones, and timeouts.
- [x] 4.2 Implement a FortiGate API client interface and HTTP client implementation for listing, creating, updating, and deleting DNS records.
- [x] 4.3 Ensure FortiGate credentials are read from secret-safe runtime configuration and are never logged.
- [x] 4.4 Add retry and error handling behavior for FortiGate timeout, authentication, validation, and server errors.
- [x] 4.5 Add mock-server tests for FortiGate request mapping, authentication handling, error handling, and non-secret logs.

## 5. Container and Kubernetes Manifests

- [x] 5.1 Add a container build file that produces a runnable controller image.
- [x] 5.2 Add example Kubernetes manifests for ServiceAccount, RBAC, Secret reference, and Deployment.
- [x] 5.3 Scope RBAC to supported core and Gateway API resources only, with no service mesh or arbitrary CRD permissions.
- [x] 5.4 Document local dry-run execution and in-cluster deployment prerequisites.

## 6. Helm Chart

- [x] 6.1 Create `charts/fortigate-external-dns` with Chart metadata, values, helpers, and templates.
- [x] 6.2 Template Deployment, ServiceAccount, RBAC, Secret or existingSecret references, and controller arguments or environment variables.
- [x] 6.3 Add values for enabled sources, namespace filters, domain filters, owner ID, dry-run, FortiGate connection options, resources, node selectors, tolerations, and affinity.
- [x] 6.4 Add chart rendering validation and tests that confirm credentials are not embedded in rendered output when `existingSecret` is used.

## 7. Public Repository Readiness

- [x] 7.1 Add README content covering purpose, FortiGate-only scope, supported resources, unsupported resources, install paths, configuration, and quick-start examples.
- [x] 7.2 Add `.gitignore` entries and documentation that prevent local FortiGate credentials and private environment files from being committed.
- [x] 7.3 Add license and attribution files compatible with any ExternalDNS-derived code or documentation.
- [x] 7.4 Add example manifests and Helm values that use placeholders or Kubernetes Secret references only.

## 8. Validation and Release Checks

- [x] 8.1 Add documented validation commands for Go tests, static checks, Helm lint or template rendering, and container build.
- [x] 8.2 Run and record the validation results for unit tests and chart rendering.
- [x] 8.3 Run a local dry-run smoke test using sample Kubernetes objects and a mock FortiGate API server.
- [x] 8.4 Confirm the repository contains no real FortiGate credentials, private IP-specific examples, or other sensitive values before public GitHub publication.
