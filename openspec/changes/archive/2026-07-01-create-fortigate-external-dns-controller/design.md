## Context

This repository will become a focused ExternalDNS-inspired controller for Kubernetes clusters that use FortiGate as the DNS management endpoint. The controller must watch supported Kubernetes exposure resources, compute desired DNS records, and reconcile those records through FortiGate APIs.

The project intentionally does not need ExternalDNS' full provider matrix or broad source matrix. The supported source scope is Kubernetes core `Service` and `Ingress` plus Kubernetes SIG Gateway API resources. Service mesh resources and unrelated third-party CRDs are out of scope. Gateway API is treated as a supported Kubernetes networking API even though it is installed as CRDs.

The project will be published publicly on a personal GitHub repository, so the implementation and examples must avoid committing FortiGate credentials, private network data, or environment-specific secrets.

## Goals / Non-Goals

**Goals:**

- Provide a Kubernetes controller that reconciles DNS records for `Service`, `Ingress`, and Gateway API hostnames.
- Keep the implementation FortiGate-specific instead of maintaining a generic DNS provider abstraction for unrelated providers.
- Preserve the useful ExternalDNS model: source discovery, desired endpoints, plan/diff, apply, dry-run, and ownership-safe cleanup.
- Package the controller as a container image with Kubernetes manifests and a Helm chart.
- Provide clear public documentation for installation, configuration, supported resources, permissions, and operational limits.

**Non-Goals:**

- Do not support AWS Route53, Google Cloud DNS, Cloudflare, webhook providers, or any other DNS backend.
- Do not watch service mesh APIs such as Istio, Linkerd, Consul, Kuma, or similar routing resources.
- Do not watch arbitrary application CRDs for hostnames.
- Do not provide a DNS server; the controller only configures FortiGate-managed DNS records.
- Do not implement multi-tenant SaaS behavior or hosted control-plane features.

## Decisions

### Decision: Build a FortiGate-only controller with ExternalDNS concepts

Use ExternalDNS' conceptual pipeline but keep the codebase scoped to one provider path: Kubernetes sources produce desired DNS endpoints, the planner computes changes, and the FortiGate client applies them.

Alternatives considered:

- Fork ExternalDNS and keep all providers and sources. This preserves upstream behavior but imports a large amount of irrelevant code, configuration, RBAC, and tests.
- Implement a small controller from scratch. This is simpler initially, but risks missing proven reconciliation concepts such as dry-run, ownership, idempotency, and safe deletion.

Rationale: A focused controller with selected ExternalDNS concepts gives the right operational behavior without carrying provider and source code that this project explicitly does not need.

### Decision: Model records internally as provider-neutral endpoints, but expose only FortiGate configuration

Use an internal desired record model with fields such as DNS name, record type, targets, TTL, source reference, zone, and owner metadata. The public configuration and apply path will remain FortiGate-only.

Alternatives considered:

- Use FortiGate API request structures as the internal model. This reduces translation code but couples source discovery to FortiGate API details.
- Keep ExternalDNS' full provider interfaces. This makes later provider expansion easier but contradicts the FortiGate-only requirement.

Rationale: A small internal model keeps Kubernetes discovery, diffing, and FortiGate transport testable without turning the project into a generic DNS provider framework.

### Decision: Restrict Kubernetes discovery to explicit source implementations

Implement separate source modules for `Service`, `Ingress`, and Gateway API resources. Do not add a generic CRD hostname scanner.

Alternatives considered:

- Reuse every ExternalDNS source. This violates the exclusion of service mesh and third-party CRD behavior.
- Build a dynamic CRD discovery mechanism. This creates surprising behavior and broader RBAC than needed.

Rationale: Explicit source modules make RBAC, behavior, documentation, and tests predictable.

### Decision: Use annotations and status fields for hostname and target extraction where appropriate

For `Service` and `Ingress`, support common ExternalDNS-style hostname and TTL annotations while also using resource status fields to determine targets. For Gateway API, use the resource hostnames and status addresses defined by the Gateway API resources.

Alternatives considered:

- Require only annotations for all resources. This is simple but ignores useful structured fields in Ingress and Gateway API.
- Infer hostnames from every possible field. This is brittle and can create records the user did not intend to publish.

Rationale: Combining explicit annotations with standard networking fields matches established Kubernetes DNS automation behavior while avoiding arbitrary inference.

### Decision: Implement ownership-safe reconciliation

The controller must mark or track records it owns so cleanup and updates do not overwrite unrelated FortiGate DNS records. The preferred strategy is configurable owner metadata using TXT records or FortiGate-supported comments/metadata where available, with deterministic ownership identifiers.

Alternatives considered:

- Delete any FortiGate record matching a discovered hostname. This is unsafe for shared zones.
- Never delete records. This avoids accidental deletion but leaves stale DNS records after Kubernetes resources are removed.

Rationale: Ownership tracking is required for a controller that safely manages non-empty DNS zones.

### Decision: Ship Helm as a first-class deployment path

Provide a chart under `charts/fortigate-external-dns` with Deployment, ServiceAccount, RBAC, Secret or existingSecret support, ConfigMap or args, and configurable resource filters.

Alternatives considered:

- Publish only raw manifests. This is easier to start with but makes repeated installation and upgrades harder.
- Publish only a Helm chart. This hides the base Kubernetes resources from users who want to inspect or customize them.

Rationale: Helm is the preferred install path, while raw examples remain useful for transparency and quick testing.

## Risks / Trade-offs

- FortiGate API differences across FortiOS versions -> Isolate FortiGate calls behind a client interface, document the tested FortiOS versions, and use integration tests or a mock server for request/response coverage.
- Ambiguous DNS target extraction for Gateway API -> Start with explicitly documented Gateway API resources and scenarios, then expand only when behavior is tested.
- Accidental modification of existing FortiGate DNS records -> Require owner ID configuration, support dry-run, and limit writes with domain or zone filters.
- Public repository may expose sensitive examples -> Use placeholders in all docs and manifests, add `.gitignore` entries for local secret files, and document Kubernetes Secret usage.
- Helm chart drift from raw manifests -> Generate or test rendered chart output in CI and keep the chart as the primary deployment artifact.

## Migration Plan

1. Initialize the Go project, controller structure, and internal endpoint model.
2. Implement Kubernetes source discovery for `Service`, `Ingress`, and Gateway API resources.
3. Implement plan/diff and dry-run behavior before any FortiGate writes.
4. Implement the FortiGate API client and ownership-safe apply behavior.
5. Add unit tests for source extraction, planning, ownership, and FortiGate API request mapping.
6. Add container image build files and local run instructions.
7. Add Kubernetes RBAC and deployment manifests.
8. Add the Helm chart and chart rendering validation.
9. Add public GitHub-ready README, examples, license, attribution, and security guidance.

Rollback for cluster deployments is standard Kubernetes rollback: scale the Deployment to zero or uninstall the Helm release. Managed DNS records remain in FortiGate unless deletion is explicitly triggered by the controller while it is running.

## Open Questions

- Which FortiOS versions and FortiGate DNS API endpoints will be the initial compatibility target?
- Should ownership use TXT records by default, FortiGate record comments where supported, or a configurable strategy?
- Which Gateway API resources are required for the first milestone beyond `Gateway` and `HTTPRoute`?
- Should the first release support only A/AAAA records, or also CNAME records where targets are DNS names?
- What default record TTL should be used when neither annotation nor chart value specifies one?
