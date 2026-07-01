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

run_helm lint ./charts/fortigate-external-dns
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --set fortigate.url=https://fortigate.example.com \
  --set fortigate.zone=example.com \
  --set fortigate.existingSecret=fortigate-external-dns \
  --set ownerID=my-cluster \
  --set domainFilters[0]=example.com > /tmp/fortigate-external-dns-rendered.yaml

if grep -q "api-token: .*token" /tmp/fortigate-external-dns-rendered.yaml; then
  echo "rendered chart appears to contain an inline API token"
  exit 1
fi

# Also render the existing-secret CI scenario so ci/existing-secret-values.yaml
# stays exercised rather than dead scaffolding.
run_helm lint --values ./charts/fortigate-external-dns/ci/existing-secret-values.yaml ./charts/fortigate-external-dns
run_helm template fortigate-external-dns ./charts/fortigate-external-dns \
  --values ./charts/fortigate-external-dns/ci/existing-secret-values.yaml > /dev/null

echo "helm template check passed"
