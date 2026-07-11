#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <owner/repository> <v-tag> <image@sha256:digest> <chart.tgz> <release-directory>" >&2
  exit 2
}

[ "$#" -eq 5 ] || usage

repository=$1
tag=$2
image_ref=$3
chart_arg=$4
release_dir_arg=$5

case "$repository" in
  */*) ;;
  *) echo "repository must be owner/name" >&2; exit 1 ;;
esac
case "$repository" in
  *[!A-Za-z0-9_.\/-]*|*/*/*|/*|*/|*..*) echo "invalid repository identity: $repository" >&2; exit 1 ;;
esac
case "$tag" in
  v[0-9]* ) ;;
  *) echo "release tag must start with v and a digit: $tag" >&2; exit 1 ;;
esac

case "$chart_arg" in
  /*) chart_path=$chart_arg ;;
  *) chart_path="$(pwd)/$chart_arg" ;;
esac
chart_dir=$(CDPATH= cd -- "$(dirname -- "$chart_path")" && pwd)
chart_name=$(basename -- "$chart_path")
chart_path="$chart_dir/$chart_name"
release_dir=$(CDPATH= cd -- "$release_dir_arg" && pwd)

[ "$chart_dir" = "$release_dir" ] || {
  echo "chart archive must be inside the release directory" >&2
  exit 1
}
case "$chart_name" in
  fortigate-external-dns-*.tgz) ;;
  *) echo "unexpected chart archive name: $chart_name" >&2; exit 1 ;;
esac
case "$chart_name" in
  *[!A-Za-z0-9._-]*) echo "unsafe chart archive name: $chart_name" >&2; exit 1 ;;
esac
[ -s "$chart_path" ] || { echo "missing chart archive: $chart_path" >&2; exit 1; }

owner=$(printf '%s' "${repository%%/*}" | tr '[:upper:]' '[:lower:]')
image_repository="ghcr.io/${owner}/fortigate-external-dns"
image_digest=${image_ref#"${image_repository}@"}
[ "$image_digest" != "$image_ref" ] || {
  echo "image must use the expected repository and an immutable digest" >&2
  exit 1
}
printf '%s\n' "$image_digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || {
  echo "image reference must end in a lowercase sha256 digest" >&2
  exit 1
}

required_files="
$chart_name.sigstore.json
image.spdx.json
chart.spdx.json
image.provenance.sigstore.json
chart.provenance.sigstore.json
image.sbom.sigstore.json
chart.sbom.sigstore.json
IMAGE_REF
IMAGE_REPOSITORY
SOURCE_COMMIT
SHA256SUMS
"
printf '%s\n' "$required_files" | while IFS= read -r file; do
  [ -z "$file" ] || [ -s "$release_dir/$file" ] || {
    echo "missing release evidence: $file" >&2
    exit 1
  }
done

[ "$(cat "$release_dir/IMAGE_REF")" = "$image_ref" ] || {
  echo "IMAGE_REF does not match the requested immutable image" >&2
  exit 1
}
[ "$(cat "$release_dir/IMAGE_REPOSITORY")" = "$image_repository" ] || {
  echo "IMAGE_REPOSITORY does not match the expected repository" >&2
  exit 1
}
source_commit=$(cat "$release_dir/SOURCE_COMMIT")
printf '%s\n' "$source_commit" | grep -Eq '^[0-9a-f]{40}$' || {
  echo "SOURCE_COMMIT must contain one full Git commit SHA" >&2
  exit 1
}

for file in "$chart_name" "$chart_name.sigstore.json" image.spdx.json chart.spdx.json \
  image.provenance.sigstore.json chart.provenance.sigstore.json \
  image.sbom.sigstore.json chart.sbom.sigstore.json IMAGE_REF IMAGE_REPOSITORY SOURCE_COMMIT; do
  grep -Eq "^[0-9a-f]{64}  ${file}$" "$release_dir/SHA256SUMS" || {
    echo "SHA256SUMS does not cover $file" >&2
    exit 1
  }
done
[ "$(wc -l < "$release_dir/SHA256SUMS" | tr -d ' ')" -eq 11 ] || {
  echo "SHA256SUMS must contain exactly the required release files" >&2
  exit 1
}
if grep -Ev '^[0-9a-f]{64}  [A-Za-z0-9._-]+$' "$release_dir/SHA256SUMS" >/dev/null; then
  echo "SHA256SUMS contains an unsafe or malformed entry" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$release_dir" && sha256sum --check --strict SHA256SUMS >/dev/null)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$release_dir" && shasum -a 256 -c SHA256SUMS >/dev/null)
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

JQ_BIN=${JQ_BIN:-jq}
COSIGN_BIN=${COSIGN_BIN:-cosign}
GH_BIN=${GH_BIN:-gh}
command -v "$JQ_BIN" >/dev/null 2>&1 || { echo "$JQ_BIN is required" >&2; exit 1; }
command -v "$COSIGN_BIN" >/dev/null 2>&1 || { echo "$COSIGN_BIN is required" >&2; exit 1; }
command -v "$GH_BIN" >/dev/null 2>&1 || { echo "$GH_BIN is required" >&2; exit 1; }

for sbom in image.spdx.json chart.spdx.json; do
  "$JQ_BIN" -e '.spdxVersion == "SPDX-2.3" and (.packages | type == "array")' \
    "$release_dir/$sbom" >/dev/null
done

issuer=https://token.actions.githubusercontent.com
identity="https://github.com/${repository}/.github/workflows/release.yml@refs/tags/${tag}"
signer_workflow="${repository}/.github/workflows/release.yml"

"$COSIGN_BIN" verify \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$image_ref" >/dev/null

"$COSIGN_BIN" verify-blob \
  --bundle "$release_dir/$chart_name.sigstore.json" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$chart_path" >/dev/null

verify_attestation() {
  subject=$1
  bundle=$2
  predicate=$3
  sbom=${4:-}
  if [ -z "$sbom" ]; then
    "$GH_BIN" attestation verify "$subject" \
      --repo "$repository" \
      --bundle "$bundle" \
      --predicate-type "$predicate" \
      --signer-workflow "$signer_workflow" \
      --source-ref "refs/tags/$tag" \
      --source-digest "$source_commit" \
      --cert-identity "$identity" \
      --cert-oidc-issuer "$issuer" >/dev/null
    return
  fi
  result=$("$GH_BIN" attestation verify "$subject" \
    --repo "$repository" \
    --bundle "$bundle" \
    --predicate-type "$predicate" \
    --signer-workflow "$signer_workflow" \
    --source-ref "refs/tags/$tag" \
    --source-digest "$source_commit" \
    --cert-identity "$identity" \
    --cert-oidc-issuer "$issuer" \
    --format json)
  expected_sbom=$("$JQ_BIN" -S -c . "$sbom")
  attested_sbom=$(printf '%s' "$result" | "$JQ_BIN" -S -c '.[0].verificationResult.statement.predicate')
  [ "$expected_sbom" = "$attested_sbom" ] || {
    echo "downloaded SBOM does not match its signed attestation: $sbom" >&2
    exit 1
  }
}

verify_attestation "oci://$image_ref" "$release_dir/image.provenance.sigstore.json" "https://slsa.dev/provenance/v1"
verify_attestation "$chart_path" "$release_dir/chart.provenance.sigstore.json" "https://slsa.dev/provenance/v1"
verify_attestation "oci://$image_ref" "$release_dir/image.sbom.sigstore.json" "https://spdx.dev/Document/v2.3" "$release_dir/image.spdx.json"
verify_attestation "$chart_path" "$release_dir/chart.sbom.sigstore.json" "https://spdx.dev/Document/v2.3" "$release_dir/chart.spdx.json"

echo "release signatures, SBOMs, and provenance verified for $repository $tag"
