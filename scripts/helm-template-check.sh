#!/usr/bin/env sh
set -eu

run_helm() {
  if [ -n "${HELM_BIN:-}" ]; then
    "$HELM_BIN" "$@"
    return
  fi
  if command -v helm >/dev/null 2>&1; then
    helm "$@"
    return
  fi
  go run helm.sh/helm/v3/cmd/helm@v3.21.2 "$@"
}

RENDER_DIR=$(mktemp -d)
trap 'rm -rf "$RENDER_DIR"' EXIT

# helm lint and template validate values against values.schema.json, so every
# render below also exercises the schema.
run_helm lint ./charts/fortigate-external-dns
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --include-crds \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' > "$RENDER_DIR/default.yaml"

if grep -q "api-token: .*token" "$RENDER_DIR/default.yaml"; then
  echo "rendered chart appears to contain an inline API token"
  exit 1
fi
if grep -q -- "--fortigate-exclusive-zone-ownership" "$RENDER_DIR/default.yaml"; then
  echo "exclusive-zone acknowledgement must not be enabled by default"
  exit 1
fi
if [ "$(grep -c '^kind: CustomResourceDefinition$' "$RENDER_DIR/default.yaml")" -ne 5 ]; then
  echo "default render must include all five structural CRDs"
  exit 1
fi
if grep -Eq '^kind: (FortiGateDNSTarget|FortiGateDNSPolicy|FortiGateDNSRecordOwnership|FortiGateDNSChangePlan|FortiGateDNSStatus)$' "$RENDER_DIR/default.yaml"; then
  echo "default render must not create platform CR instances"
  exit 1
fi
if grep -q 'resources: \["endpointslices"\]' "$RENDER_DIR/default.yaml"; then
  echo "default RBAC must not enable EndpointSlice access"
  exit 1
fi
if grep -q 'checksum/platform-values:' "$RENDER_DIR/default.yaml"; then
  echo "default Deployment must not opt into platform configuration"
  exit 1
fi
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set dryRun=false >/dev/null 2>&1; then
  echo "write mode without exclusive-zone acknowledgement must fail to render"
  exit 1
fi

# Probes must be independent of metrics exposure: a metrics-disabled render
# still carries liveness/readiness probes and the probe-server bind.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set metrics.enabled=false > "$RENDER_DIR/metrics-disabled.yaml"

for probe in livenessProbe readinessProbe; do
  if ! grep -q "$probe" "$RENDER_DIR/metrics-disabled.yaml"; then
    echo "metrics.enabled=false must not remove the $probe"
    exit 1
  fi
done
if ! grep -q -- "--metrics-addr=:8080" "$RENDER_DIR/metrics-disabled.yaml"; then
  echo "metrics.enabled=false must not disable the probe server bind"
  exit 1
fi
if grep -q "^kind: Service$" "$RENDER_DIR/metrics-disabled.yaml"; then
  echo "metrics.enabled=false must not render the metrics Service"
  exit 1
fi

# Egress NetworkPolicy renders with the FortiGate peer when enabled.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set egressNetworkPolicy.enabled=true \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set egressNetworkPolicy.kubeAPI.cidr=10.96.0.1/32 \
  --set egressNetworkPolicy.dns.cidr=10.96.0.10/32 > "$RENDER_DIR/egress.yaml"

for cidr in 203.0.113.10/32 10.96.0.1/32 10.96.0.10/32; do
  if ! grep -q "cidr: \"$cidr\"" "$RENDER_DIR/egress.yaml"; then
    echo "egress NetworkPolicy must include peer CIDR: $cidr"
    exit 1
  fi
done

expect_egress_failure() {
  name="$1"
  shift
  if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
    --set fortigate.url=https://fortigate.example.com \
    --set fortigate.zone=example.com \
    --set fortigate.existingSecret=fortigate-external-dns \
    --set ownerID=my-cluster \
    --set egressNetworkPolicy.enabled=true \
    "$@" >/dev/null 2>&1; then
    echo "egress NetworkPolicy must reject: $name"
    exit 1
  fi
}

expect_egress_failure "missing FortiGate CIDR" \
  --set egressNetworkPolicy.kubeAPI.cidr=10.96.0.1/32 \
  --set egressNetworkPolicy.dns.cidr=10.96.0.10/32
expect_egress_failure "missing Kubernetes API CIDR" \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set egressNetworkPolicy.dns.cidr=10.96.0.10/32
expect_egress_failure "missing enabled DNS CIDR" \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set egressNetworkPolicy.kubeAPI.cidr=10.96.0.1/32
expect_egress_failure "empty Kubernetes API ports" \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set egressNetworkPolicy.kubeAPI.cidr=10.96.0.1/32 \
  --set egressNetworkPolicy.dns.cidr=10.96.0.10/32 \
  --set-json 'egressNetworkPolicy.kubeAPI.ports=[]'

expect_duration_failure() {
  value="$1"
  if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
    --set fortigate.url=https://fortigate.example.com \
    --set fortigate.zone=example.com \
    --set fortigate.existingSecret=fortigate-external-dns \
    --set ownerID=my-cluster \
    --set "$value" >/dev/null 2>&1; then
    echo "values schema must reject zero duration: $value"
    exit 1
  fi
}

expect_duration_failure interval=0s
expect_duration_failure interval=0.1ns
expect_duration_failure reconcileTimeout=0s
expect_duration_failure fortigate.timeout=0s
expect_duration_failure healthzMaxStaleness=0.1ns

# CA bundle renders a ConfigMap, mount, and the --fortigate-ca-file flag.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set 'domainFilters[0]=example.com' \
  --set-string 'podAnnotations.checksum/fortigate-ca=operator-override' \
  --set-string fortigate.caBundle='-----BEGIN CERTIFICATE-----
unit-test
-----END CERTIFICATE-----' > "$RENDER_DIR/ca.yaml"

for needle in "fortigate-ca-file=/etc/fortigate-external-dns/ca/ca.crt" "kind: ConfigMap" "BEGIN CERTIFICATE" "checksum/fortigate-ca:"; do
  if ! grep -q -- "$needle" "$RENDER_DIR/ca.yaml"; then
    echo "CA bundle render is missing: $needle"
    exit 1
  fi
done

run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set-string fortigate.caBundle='-----BEGIN CERTIFICATE-----
rotated-unit-test
-----END CERTIFICATE-----' > "$RENDER_DIR/ca-rotated.yaml"

ca_checksum=$(awk '/checksum\/fortigate-ca:/ { value=$2 } END { print value }' "$RENDER_DIR/ca.yaml")
rotated_ca_checksum=$(awk '/checksum\/fortigate-ca:/ { value=$2 } END { print value }' "$RENDER_DIR/ca-rotated.yaml")
if [ -z "$ca_checksum" ] || [ -z "$rotated_ca_checksum" ] || [ "$ca_checksum" = 'operator-override' ] || [ "$ca_checksum" = '"operator-override"' ] || [ "$ca_checksum" = "$rotated_ca_checksum" ]; then
  echo "changing fortigate.caBundle must change the Pod template checksum"
  exit 1
fi

# The exclusive-zone acknowledgement is opt-in and must reach the controller.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set fortigate.exclusiveZoneOwnership=true > "$RENDER_DIR/exclusive-zone.yaml"
if ! grep -q -- "--fortigate-exclusive-zone-ownership" "$RENDER_DIR/exclusive-zone.yaml"; then
  echo "exclusive-zone acknowledgement must render the controller flag"
  exit 1
fi

# Contradictory trust configuration must fail the render.
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set fortigate.insecureSkipVerify=true \
  --set-string fortigate.caBundle='x' >/dev/null 2>&1; then
  echo "caBundle combined with insecureSkipVerify must fail to render"
  exit 1
fi

# Also render the existing-secret CI scenario so ci/existing-secret-values.yaml
# stays exercised rather than dead scaffolding, plus the documented sample.
run_helm lint --values ./charts/fortigate-external-dns/ci/existing-secret-values.yaml ./charts/fortigate-external-dns
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./charts/fortigate-external-dns/ci/existing-secret-values.yaml > /dev/null
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.existingSecret=fortigate-external-dns \
  --values ./samples/values-existing-secret.yaml > /dev/null

# Representative staged platform render: multi-target, shared ownership,
# approval, policy, event/watch, source expansion, status, and monitoring.
run_helm lint --values ./samples/platform-values.yaml ./charts/fortigate-external-dns
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --include-crds \
  --values ./samples/platform-values.yaml > "$RENDER_DIR/platform.yaml"

for kind in FortiGateDNSTarget FortiGateDNSPolicy PrometheusRule ConfigMap; do
  if ! grep -q "^kind: $kind$" "$RENDER_DIR/platform.yaml"; then
    echo "platform render is missing kind: $kind"
    exit 1
  fi
done
if [ "$(grep -c '^kind: FortiGateDNSTarget$' "$RENDER_DIR/platform.yaml")" -ne 2 ]; then
  echo "multi-target example must render two target CRs"
  exit 1
fi
for resource in endpointslices fortigatednstargets fortigatednsrecordownerships/status fortigatednschangeplans/finalizers fortigatednsstatuses/finalizers; do
  if ! grep -q "$resource" "$RENDER_DIR/platform.yaml"; then
    echo "platform RBAC is missing resource: $resource"
    exit 1
  fi
done
for credential in edge-fortigate-credentials internal-fortigate-credentials edge-fortigate-ca; do
  if ! grep -q -- "- $credential" "$RENDER_DIR/platform.yaml"; then
    echo "platform RBAC is missing resourceName: $credential"
    exit 1
  fi
done
if ! grep -q 'checksum/platform-values:' "$RENDER_DIR/platform.yaml"; then
  echo "platform configuration must participate in the Pod rollout checksum"
  exit 1
fi
for flag in --target-mode --platform-namespace=default --policy-enforcement --event-driven --debounce=2s --resync=1m --status-retention=20 --publish-external-name-services --publish-headless-services; do
  if ! grep -q -- "$flag" "$RENDER_DIR/platform.yaml"; then
    echo "platform runtime render is missing flag: $flag"
    exit 1
  fi
done
if grep -q -- '--fortigate-url=' "$RENDER_DIR/platform.yaml" || grep -q 'name: FORTIGATE_API_TOKEN' "$RENDER_DIR/platform.yaml"; then
  echo "target mode must not pass direct FortiGate connection settings"
  exit 1
fi

# rbac.create=false must remove every namespaced and cluster-scoped RBAC object.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/platform-values.yaml \
  --set rbac.create=false > "$RENDER_DIR/rbac-disabled.yaml"
if grep -Eq '^kind: (Role|RoleBinding|ClusterRole|ClusterRoleBinding)$' "$RENDER_DIR/rbac-disabled.yaml"; then
  echo "rbac.create=false must render no RBAC objects"
  exit 1
fi

# Headless-only mode grants EndpointSlice reads and enables the supported runtime gate.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set platform.sourceExpansion.headless.enabled=true > "$RENDER_DIR/headless.yaml"
if ! grep -q 'resources: \["endpointslices"\]' "$RENDER_DIR/headless.yaml" || ! grep -q -- '--publish-headless-services' "$RENDER_DIR/headless.yaml"; then
  echo "headless mode must add EndpointSlice RBAC and the runtime gate"
  exit 1
fi

expect_platform_failure() {
  name="$1"
  shift
  if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
    --values ./samples/platform-values.yaml \
    "$@" >/dev/null 2>&1; then
    echo "platform values must reject: $name"
    exit 1
  fi
}

expect_platform_failure "target mode without targets" \
  --set-json 'platform.targetMode.targets=[]'
expect_platform_failure "shared target without shared ownership gate" \
  --set platform.sharedOwnership.enabled=false
expect_platform_failure "approval target without plan approval gate" \
  --set platform.planApproval.enabled=false
expect_platform_failure "credential Secret absent from RBAC allowlist" \
  --set-json 'platform.targetMode.apiTokenSecretNames=["internal-fortigate-credentials"]'
expect_platform_failure "optional credential Secret reference" \
  --set platform.targetMode.targets[0].apiTokenSecretRef.optional=true
expect_platform_failure "secret-bearing target URL" \
  --set platform.targetMode.targets[0].url=https://user:password@fortigate.example.com
expect_platform_failure "unsafe overlapping target scopes" \
  --set 'platform.targetMode.targets[1].domainFilters[0]=edge.example.com'
expect_platform_failure "policies configured while policy mode is disabled" \
  --set platform.policy.enabled=false
expect_platform_failure "zero event debounce" \
  --set platform.events.debounce=0s
expect_platform_failure "unbounded status retention" \
  --set platform.status.retention=101

if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set-json 'sources=["ingress"]' \
  --set platform.sourceExpansion.headless.enabled=true >/dev/null 2>&1; then
  echo "headless staging without the service source must fail schema validation"
  exit 1
fi

ruby -e '
  require "json"
  require "yaml"
  docs = YAML.load_stream(File.read(ARGV.fetch(0))).compact
  dashboard = docs.find { |doc| doc["kind"] == "ConfigMap" && doc.dig("metadata", "name").to_s.end_with?("-grafana-dashboard") }
  abort "Grafana dashboard ConfigMap missing" unless dashboard
  parsed = JSON.parse(dashboard.dig("data", "fortigate-external-dns.json"))
  abort "Grafana dashboard has no panels" unless parsed["panels"].is_a?(Array) && !parsed["panels"].empty?
  rule = docs.find { |doc| doc["kind"] == "PrometheusRule" }
  alerts = rule&.dig("spec", "groups")&.flat_map { |group| group.fetch("rules", []) }&.map { |entry| entry["alert"] }&.compact
  required = %w[FortiGateExternalDNSReconcileStale FortiGateExternalDNSProviderUnreachable FortiGateExternalDNSOwnershipConflict FortiGateExternalDNSPlanPendingApproval FortiGateExternalDNSDiscoveryIncomplete FortiGateExternalDNSCleanupRefused]
  abort "PrometheusRule alert set drifted" unless alerts&.sort == required.sort
' "$RENDER_DIR/platform.yaml"

echo "helm template check passed"
