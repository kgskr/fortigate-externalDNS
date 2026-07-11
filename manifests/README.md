# Raw manifest installation paths

The raw `deployment.yaml`, `serviceaccount.yaml`, and `rbac.yaml` files are the
supported compatibility single-target path. They remain dry-run, namespace
restricted, and `cleanup-policy=keep` by default and do not enable CRD target,
shared ownership, policy, approval, or Service source-expansion modes.

Install the platform APIs before enabling target mode:

```sh
kubectl apply -f manifests/crds/fortigate-external-dns.yaml
```

`platform-rbac.yaml` is an explicit operator patch for the `default` namespace.
It adds EndpointSlice reads, controller-CR/status/finalizer access, and
resourceName-bound reads for the example credential Secret and CA ConfigMap.
Edit all names/namespaces to match your target references before applying it.
The bundled raw Deployment stays on direct single-target arguments. To activate
target mode, apply the CRDs and an environment-specific least-privilege version
of `platform-rbac.yaml`, then replace direct FortiGate arguments with
`--target-mode --platform-namespace=<namespace>` and add only the required
policy, event, status, ExternalName, or headless flags. The Helm chart performs
this wiring automatically and is preferred for platform deployments.

Before a platform migration, back up FortiGate separately and export
target/claim/plan/status metadata without Secret contents. During
decommissioning or CRD-loss recovery, stop writers before changing CRDs or
finalizers. Missing claims never authorize provider deletion; restore APIs and
metadata, re-list FortiGate, and let the controller revalidate exact identities.
Status and completed-plan audit retention is bounded; never prune pending,
approved, applying, or interrupted plans as terminal history.

Shared create/update/delete operations are claim-gated. A target or record-type
replacement changes record identity and requires a separate adoption/replacement
plan with exact-hash approval; an existing claim is not sufficient.

When changing the install namespace, update every namespace in the Deployment,
ServiceAccount, RoleBindings, leader-election arguments, samples, and optional
platform RBAC together.
