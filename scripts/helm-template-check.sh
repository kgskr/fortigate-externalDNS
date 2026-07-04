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
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com > "$RENDER_DIR/default.yaml"

if grep -q "api-token: .*token" "$RENDER_DIR/default.yaml"; then
  echo "rendered chart appears to contain an inline API token"
  exit 1
fi

# Probes must be independent of metrics exposure: a metrics-disabled render
# still carries liveness/readiness probes and the probe-server bind.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com \
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
  --set domainFilters[0]=example.com \
  --set egressNetworkPolicy.enabled=true \
  --set egressNetworkPolicy.fortigate.cidr=203.0.113.10/32 > "$RENDER_DIR/egress.yaml"

if ! grep -q "cidr: 203.0.113.10/32" "$RENDER_DIR/egress.yaml"; then
  echo "egress NetworkPolicy must include the FortiGate peer CIDR"
  exit 1
fi

# CA bundle renders a ConfigMap, mount, and the --fortigate-ca-file flag.
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com \
  --set-string fortigate.caBundle='-----BEGIN CERTIFICATE-----
unit-test
-----END CERTIFICATE-----' > "$RENDER_DIR/ca.yaml"

for needle in "fortigate-ca-file=/etc/fortigate-external-dns/ca/ca.crt" "kind: ConfigMap" "BEGIN CERTIFICATE"; do
  if ! grep -q -- "$needle" "$RENDER_DIR/ca.yaml"; then
    echo "CA bundle render is missing: $needle"
    exit 1
  fi
done

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

echo "helm template check passed"
