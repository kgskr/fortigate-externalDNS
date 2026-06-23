## Why

Kubernetes workloads exposed through Services, Ingresses, and Gateway API resources need DNS records to be created and kept in sync without manual FortiGate DNS administration. This project will create a focused ExternalDNS-inspired controller that only targets FortiGate DNS APIs and avoids the complexity of unrelated DNS providers and unsupported Kubernetes resource sources.

## What Changes

- Build a Kubernetes controller container that derives desired DNS records from supported Kubernetes resources.
- Support core Kubernetes `Service` and `Ingress` sources.
- Support Kubernetes SIG Gateway API resources needed to publish hostnames, such as `Gateway` and route resources.
- Exclude service mesh resources and third-party application CRDs from source discovery.
- Manage DNS records only through FortiGate device APIs.
- Remove or avoid generic multi-provider DNS abstractions that are not required for FortiGate-only operation.
- Add safe reconciliation behavior for create, update, and delete events, including ownership tracking for records managed by this controller.
- Provide container packaging and Kubernetes deployment assets.
- Provide a Helm chart for installation and configuration.
- Prepare the repository for public GitHub publishing, including documentation, license compliance, example manifests, and secret-safe configuration guidance.

## Capabilities

### New Capabilities

- `kubernetes-resource-discovery`: Discovers DNS intent from supported Kubernetes `Service`, `Ingress`, and Gateway API resources while explicitly excluding service mesh resources and unrelated third-party CRDs.
- `fortigate-dns-record-management`: Reconciles desired DNS records against FortiGate DNS through FortiGate APIs, including create, update, delete, dry-run, ownership, and error handling behavior.
- `deployment-packaging`: Packages the controller as a containerized Kubernetes application with RBAC, configuration, Helm chart support, public repository documentation, and release-ready project metadata.

### Modified Capabilities

- None.

## Impact

- New Go controller code based on the ExternalDNS reconciliation model, scoped down to FortiGate-only behavior.
- Kubernetes client dependencies for core resources and Gateway API types.
- FortiGate API client implementation and configuration surface for device URL, credentials or token, VDOM or DNS context, TLS behavior, zones, and managed record filters.
- Container image build files and Kubernetes manifests.
- Helm chart values, templates, RBAC, and deployment documentation.
- Public GitHub readiness work, including README, examples, license and attribution handling for any ExternalDNS-derived code, and clear guidance to keep FortiGate credentials out of source control.
