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
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=http://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster >/dev/null 2>&1; then
  echo "cleartext FortiGate URL must fail schema validation"
  exit 1
fi

# Disabling ServiceAccount creation must name an explicit least-privilege
# account. Falling back to the namespace's default account is forbidden.
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set serviceAccount.create=false >/dev/null 2>&1; then
  echo "serviceAccount.create=false without an explicit name must fail to render"
  exit 1
fi
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set serviceAccount.create=false \
  --set serviceAccount.name=fortigate-external-dns-runtime > "$RENDER_DIR/named-service-account.yaml"
if ! grep -Eq '^[[:space:]]+serviceAccountName: fortigate-external-dns-runtime$' "$RENDER_DIR/named-service-account.yaml" || \
   ! grep -Eq '^[[:space:]]+name: fortigate-external-dns-runtime$' "$RENDER_DIR/named-service-account.yaml"; then
  echo "explicit ServiceAccount name must reach the Pod and RBAC bindings"
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
for resource in endpointslices fortigatednstargets fortigatednsrecordownerships/status fortigatednsrecordownerships/finalizers fortigatednschangeplans/status fortigatednsstatuses/status; do
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
for flag in --target-mode --platform-namespace=default --policy-enforcement --event-driven --debounce=2s --resync=1m --status-retention=20 --plan-retention=20 --publish-external-name-services --publish-headless-services; do
  if ! grep -q -- "$flag" "$RENDER_DIR/platform.yaml"; then
    echo "platform runtime render is missing flag: $flag"
    exit 1
  fi
done
if grep -q -- '--fortigate-url=' "$RENDER_DIR/platform.yaml" || grep -q 'name: FORTIGATE_API_TOKEN' "$RENDER_DIR/platform.yaml"; then
  echo "target mode must not pass direct FortiGate connection settings"
  exit 1
fi

# Policy enforcement also works in legacy mode, and policy reads follow the
# same namespace scope as source discovery.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set platform.policy.enabled=true \
  --set platform.policy.policies[0].name=team-a-policy \
  --set platform.policy.policies[0].namespace=team-a \
  --set 'namespaces[0]=team-a' \
  --set 'namespaces[1]=team-b' > "$RENDER_DIR/legacy-policy-namespaced.yaml"
if ! grep -q -- '--policy-enforcement' "$RENDER_DIR/legacy-policy-namespaced.yaml" || \
   [ "$(grep -c 'resources: \["fortigatednspolicies"\]' "$RENDER_DIR/legacy-policy-namespaced.yaml")" -ne 2 ]; then
  echo "legacy namespaced policy mode must add the flag and one policy Role per source namespace"
  exit 1
fi
ruby -ryaml -e '
  [ARGV[0], ARGV[1]].each_with_index do |path, index|
    docs = YAML.load_stream(File.read(path)).compact
    role = docs.find { |d| d["kind"] == "ClusterRole" && d.dig("metadata", "name").end_with?("-gateway-namespaces") }
    abort "missing Gateway namespace label read grant" unless role
    rule = role.fetch("rules").first
    abort "namespace grant must only get labels" unless rule["resources"] == ["namespaces"] && rule["verbs"] == ["get"]
    names = rule["resourceNames"]
    abort "namespace label grant scope differs from sources" unless index == 0 ? names.nil? : names.sort == ["team-a", "team-b"]
    abort "namespace grant is not bound" unless docs.any? { |d| d["kind"] == "ClusterRoleBinding" && d.dig("roleRef", "name") == role.dig("metadata", "name") }
  end
' "$RENDER_DIR/default.yaml" "$RENDER_DIR/legacy-policy-namespaced.yaml"
for namespace in team-a team-b; do
  run_helm template dns ./charts/fortigate-external-dns --namespace "$namespace" \
    --show-only templates/rbac.yaml \
    --set fortigate.url=https://fortigate.example.com \
    --set fortigate.zone=example.com \
    --set fortigate.existingSecret=fortigate-external-dns \
    --set ownerID=my-cluster \
    --set "namespaces[0]=$namespace" > "$RENDER_DIR/gateway-$namespace.yaml"
done
ruby -ryaml -e '
  names = ARGV.map do |path|
    YAML.load_stream(File.read(path)).compact.select { |d| ["ClusterRole", "ClusterRoleBinding"].include?(d["kind"]) }.map { |d| [d["kind"], d.dig("metadata", "name")] }
  end
  abort "namespaced releases collide on Gateway namespace RBAC" unless (names[0] & names[1]).empty?
' "$RENDER_DIR/gateway-team-a.yaml" "$RENDER_DIR/gateway-team-b.yaml"
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set platform.policy.enabled=true \
  --set platform.policy.policies[0].name=default-policy > "$RENDER_DIR/legacy-policy-clusterwide.yaml"
if [ "$(grep -c 'resources: \["fortigatednspolicies"\]' "$RENDER_DIR/legacy-policy-clusterwide.yaml")" -ne 1 ]; then
  echo "legacy cluster-wide policy mode must add one policy ClusterRole rule"
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

# --- Writer safety: replicas, one-shot Job, scheduling knobs ---------------
BASE_SET="--set fortigate.url=https://fortigate.example.com --set fortigate.zone=example.com --set fortigate.existingSecret=fortigate-external-dns --set ownerID=my-cluster"

# More than one replica without leader election means multiple writers.
# shellcheck disable=SC2086
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set replicaCount=2 --set leaderElection.enabled=false >/dev/null 2>&1; then
  echo "replicaCount>1 without leader election must fail to render"
  exit 1
fi
# shellcheck disable=SC2086
run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set replicaCount=2 > "$RENDER_DIR/replicas.yaml"
if ! grep -q '^kind: Deployment$' "$RENDER_DIR/replicas.yaml" || ! grep -q 'replicas: 2' "$RENDER_DIR/replicas.yaml"; then
  echo "replicaCount=2 with leader election must render a Deployment"
  exit 1
fi

# once=true renders a Job (no Deployment) named with the release revision.
# shellcheck disable=SC2086
run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set once=true --set replicaCount=1 > "$RENDER_DIR/once.yaml"
if grep -q '^kind: Deployment$' "$RENDER_DIR/once.yaml" || ! grep -q '^kind: Job$' "$RENDER_DIR/once.yaml"; then
  echo "once=true must render a Job instead of a Deployment"
  exit 1
fi
for needle in 'restartPolicy: Never' 'backoffLimit: 0' 'name: fortigate-external-dns-r1$' '- --once$'; do
  if ! grep -Eq -- "$needle" "$RENDER_DIR/once.yaml"; then
    echo "once Job render is missing: $needle"
    exit 1
  fi
done
if grep -Eq 'livenessProbe|readinessProbe' "$RENDER_DIR/once.yaml"; then
  echo "once Job must not carry probes"
  exit 1
fi

# Scheduling knobs: priorityClassName, topologySpreadConstraints, and a PDB
# that only renders for leader-elected multi-replica installs.
# shellcheck disable=SC2086
run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set replicaCount=2 \
  --set priorityClassName=system-cluster-critical \
  --set-json 'topologySpreadConstraints=[{"maxSkew":1,"topologyKey":"topology.kubernetes.io/zone","whenUnsatisfiable":"ScheduleAnyway","labelSelector":{"matchLabels":{"app.kubernetes.io/name":"fortigate-external-dns"}}}]' \
  --set podDisruptionBudget.enabled=true > "$RENDER_DIR/scheduling.yaml"
for needle in 'priorityClassName: "system-cluster-critical"' 'topologySpreadConstraints:' 'kind: PodDisruptionBudget' 'minAvailable: 1'; do
  if ! grep -q -- "$needle" "$RENDER_DIR/scheduling.yaml"; then
    echo "scheduling render is missing: $needle"
    exit 1
  fi
done
# shellcheck disable=SC2086
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set podDisruptionBudget.enabled=true >/dev/null 2>&1; then
  echo "podDisruptionBudget on a single replica must fail to render"
  exit 1
fi
# shellcheck disable=SC2086
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set replicaCount=2 --set leaderElection.enabled=false \
  --set once=true --set podDisruptionBudget.enabled=true >/dev/null 2>&1; then
  echo "podDisruptionBudget with once=true must fail to render"
  exit 1
fi
if grep -q 'kind: PodDisruptionBudget' "$RENDER_DIR/default.yaml"; then
  echo "PodDisruptionBudget must be opt-in"
  exit 1
fi

# --- Target mode: guards and status RBAC are scoped to the right mode ------
# dryRun/fortigate.* are ignored in target mode, so the exclusive-zone guard
# must not fire there.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/platform-values.yaml \
  --set dryRun=false > "$RENDER_DIR/target-dryrun-false.yaml"
if grep -q 'rollout restart' "$RENDER_DIR/target-dryrun-false.yaml"; then
  echo "target-mode render must not print direct-mode Secret restart text"
  exit 1
fi
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/platform-values.yaml \
  --set platform.status.enabled=false > "$RENDER_DIR/target-status-off.yaml"
if ! grep -q 'resources: \["fortigatednsstatuses"\]' "$RENDER_DIR/target-status-off.yaml" || \
   ! grep -q 'resources: \["fortigatednsstatuses/status"\]' "$RENDER_DIR/target-status-off.yaml"; then
  echo "target mode alone must grant fortigatednsstatuses RBAC"
  exit 1
fi
# shellcheck disable=SC2086
run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set platform.status.enabled=false > "$RENDER_DIR/legacy-no-status.yaml"
if grep -q 'fortigatednsstatuses' "$RENDER_DIR/legacy-no-status.yaml"; then
  echo "direct mode without status must not grant fortigatednsstatuses RBAC"
  exit 1
fi

run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/platform-values.yaml > "$RENDER_DIR/platform-rbac-only.yaml"
# Trimmed RBAC: no Kubernetes Events, no patch, no target status/finalizers,
# no status delete; claim delete and finalizer update are retained.
for forbidden in '"events"' '"patch"' 'fortigatednstargets/status' 'fortigatednstargets/finalizers' 'fortigatednschangeplans/finalizers' 'fortigatednsstatuses/finalizers'; do
  if grep -q -- "$forbidden" "$RENDER_DIR/platform-rbac-only.yaml"; then
    echo "platform RBAC must not grant unused permission: $forbidden"
    exit 1
  fi
done
ruby -e '
  require "yaml"
  docs = YAML.load_stream(File.read(ARGV.fetch(0))).compact
  rules = docs.select { |d| d["kind"] == "Role" }.flat_map { |d| d["rules"] }
  find = ->(res) { rules.find { |r| Array(r["resources"]) == [res] } }
  claims = find.call("fortigatednsrecordownerships") or abort "claims rule missing"
  abort "claims need delete" unless claims["verbs"].include?("delete")
  abort "claims need finalizers update" unless find.call("fortigatednsrecordownerships/finalizers")&.fetch("verbs") == ["update"]
  plans = find.call("fortigatednschangeplans") or abort "plans rule missing"
  abort "plans need delete" unless plans["verbs"].include?("delete")
  statuses = find.call("fortigatednsstatuses") or abort "statuses rule missing"
  abort "statuses must not delete" if statuses["verbs"].include?("delete")
' "$RENDER_DIR/platform-rbac-only.yaml"

# Headless RBAC depends only on the runtime gate, not chart-managed targets.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/platform-values.yaml \
  --set platform.targetMode.targets[0].headlessEnabled=false \
  --set platform.targetMode.targets[1].headlessEnabled=false > "$RENDER_DIR/headless-untargeted.yaml"
if ! grep -q 'resources: \["endpointslices"\]' "$RENDER_DIR/headless-untargeted.yaml"; then
  echo "headless.enabled must grant EndpointSlice RBAC regardless of chart-managed target headlessEnabled"
  exit 1
fi

# --- Egress NetworkPolicy: list form, legacy compatibility, DNS selector ---
# shellcheck disable=SC2086
run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set egressNetworkPolicy.enabled=true \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set-json 'egressNetworkPolicy.fortigate.cidrs=["203.0.113.11/32","198.51.100.0/24"]' \
  --set-json 'egressNetworkPolicy.fortigate.ports=[443,8443]' \
  --set-json 'egressNetworkPolicy.kubeAPI.cidrs=["192.0.2.10/32","192.0.2.11/32"]' \
  --set-json 'egressNetworkPolicy.dns.namespaceSelector={"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}}' \
  --set-json 'egressNetworkPolicy.dns.podSelector={"matchLabels":{"k8s-app":"kube-dns"}}' > "$RENDER_DIR/egress-list.yaml"
for cidr in 203.0.113.10/32 203.0.113.11/32 198.51.100.0/24 192.0.2.10/32 192.0.2.11/32; do
  if ! grep -q "cidr: \"$cidr\"" "$RENDER_DIR/egress-list.yaml"; then
    echo "egress list form must include CIDR: $cidr"
    exit 1
  fi
done
for needle in 'port: 8443' 'namespaceSelector:' 'k8s-app: kube-dns'; do
  if ! grep -q -- "$needle" "$RENDER_DIR/egress-list.yaml"; then
    echo "egress render is missing: $needle"
    exit 1
  fi
done
ruby -e '
  require "yaml"
  docs = YAML.load_stream(File.read(ARGV.fetch(0))).compact
  policy = docs.find { |d| d["kind"] == "NetworkPolicy" }
  dns = policy.dig("spec", "egress").find { |r| Array(r["ports"]).any? { |p| p["port"] == 53 } }
  peer = dns["to"].first
  abort "DNS selector peer must combine namespaceSelector and podSelector" unless peer["namespaceSelector"] && peer["podSelector"] && !peer["ipBlock"]
' "$RENDER_DIR/egress-list.yaml"
expect_egress_failure "DNS enabled without any peer" \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 \
  --set egressNetworkPolicy.kubeAPI.cidr=192.0.2.10/32

# --- Monitoring: aggregated staleness, absent alert, ServiceMonitor --------
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./samples/monitoring-values.yaml > "$RENDER_DIR/monitoring.yaml"
if ! grep -q 'time() - max by (namespace) (fortigate_external_dns_last_successful_reconcile_timestamp_seconds{' "$RENDER_DIR/monitoring.yaml"; then
  echo "ReconcileStale must aggregate across replicas with max by (namespace)"
  exit 1
fi
if ! grep -q 'absent(fortigate_external_dns_build_info{' "$RENDER_DIR/monitoring.yaml"; then
  echo "an absent(build_info) alert is required"
  exit 1
fi
for needle in 'kind: ServiceMonitor' 'release: kube-prometheus-stack' 'interval: 30s' 'scrapeTimeout: 10s' 'port: metrics'; do
  if ! grep -q -- "$needle" "$RENDER_DIR/monitoring.yaml"; then
    echo "ServiceMonitor render is missing: $needle"
    exit 1
  fi
done
if grep -q 'kind: ServiceMonitor' "$RENDER_DIR/default.yaml"; then
  echo "ServiceMonitor must be opt-in"
  exit 1
fi
# shellcheck disable=SC2086
if run_helm template fortigate-external-dns ./charts/fortigate-external-dns $BASE_SET \
  --set monitoring.serviceMonitor.enabled=true >/dev/null 2>&1; then
  echo "ServiceMonitor without the metrics Service must fail to render"
  exit 1
fi

# --- CRD content: CIDR validation, target retries default, twin copies ------
if ! cmp -s ./charts/fortigate-external-dns/crds/fortigate-external-dns.yaml ./manifests/crds/fortigate-external-dns.yaml; then
  echo "chart and raw CRD copies must be byte-identical"
  exit 1
fi
if ! grep -q "rule: 'isCIDR(self)'" ./charts/fortigate-external-dns/crds/fortigate-external-dns.yaml; then
  echo "allowedTargetCIDRs items must be validated with isCIDR"
  exit 1
fi
if ! grep -q 'retries: {type: integer, format: int32, minimum: 0, maximum: 10, default: 2}' ./charts/fortigate-external-dns/crds/fortigate-external-dns.yaml; then
  echo "FortiGateDNSTarget spec.retries must default to 2"
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
  required = %w[FortiGateExternalDNSReconcileStale FortiGateExternalDNSMetricsAbsent FortiGateExternalDNSProviderUnreachable FortiGateExternalDNSOwnershipConflict FortiGateExternalDNSPlanPendingApproval FortiGateExternalDNSDiscoveryIncomplete FortiGateExternalDNSCleanupRefused]
  abort "PrometheusRule alert set drifted" unless alerts&.sort == required.sort
' "$RENDER_DIR/platform.yaml"

echo "helm template check passed"
