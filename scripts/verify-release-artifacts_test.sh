#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
verifier="$root/scripts/verify-release-artifacts.sh"
tmp_base=${TMPDIR:-/tmp}
tmp=$(mktemp -d "${tmp_base%/}/release-evidence.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin"

cat > "$tmp/bin/cosign" <<'EOF'
#!/bin/sh
set -eu
command=$1
shift
bundle=${FAKE_IMAGE_SIGNATURE:-}
identity=
issuer=
subject=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift 2 ;;
    --certificate-identity) identity=$2; shift 2 ;;
    --certificate-oidc-issuer) issuer=$2; shift 2 ;;
    --*) shift ;;
    *) subject=$1; shift ;;
  esac
done
[ -s "$bundle" ]
value() { sed -n "s/^$1=//p" "$bundle"; }
[ "$identity" = "$(value identity)" ]
[ "$issuer" = "$(value issuer)" ]
[ "$subject" = "$(value subject)" ]
if [ "$command" = verify-blob ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    digest=$(sha256sum "$subject" | awk '{print $1}')
  else
    digest=$(shasum -a 256 "$subject" | awk '{print $1}')
  fi
  [ "$digest" = "$(value artifact_sha)" ]
else
  [ "$command" = verify ]
fi
EOF

cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
set -eu
[ "$1" = attestation ]
[ "$2" = verify ]
subject=$3
shift 3
bundle=
repository=
predicate=
workflow=
source_ref=
source_digest=
identity=
issuer=
format=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift 2 ;;
    --repo) repository=$2; shift 2 ;;
    --predicate-type) predicate=$2; shift 2 ;;
    --signer-workflow) workflow=$2; shift 2 ;;
    --source-ref) source_ref=$2; shift 2 ;;
    --source-digest) source_digest=$2; shift 2 ;;
    --cert-identity) identity=$2; shift 2 ;;
    --cert-oidc-issuer) issuer=$2; shift 2 ;;
    --format) format=$2; shift 2 ;;
    *) exit 1 ;;
  esac
done
[ -s "$bundle" ]
value() { sed -n "s/^$1=//p" "$bundle"; }
[ "$subject" = "$(value subject)" ]
[ "$repository" = "$(value repository)" ]
[ "$predicate" = "$(value predicate)" ]
[ "$workflow" = "$(value workflow)" ]
[ "$source_ref" = "$(value source_ref)" ]
[ "$source_digest" = "$(value source_digest)" ]
[ "$identity" = "$(value identity)" ]
[ "$issuer" = "$(value issuer)" ]
case "$subject" in
  oci://*) ;;
  *)
    if command -v sha256sum >/dev/null 2>&1; then
      digest=$(sha256sum "$subject" | awk '{print $1}')
    else
      digest=$(shasum -a 256 "$subject" | awk '{print $1}')
    fi
    [ "$digest" = "$(value artifact_sha)" ]
    ;;
esac
if [ "$format" = json ]; then
  sbom_path=$(value sbom_path)
  [ -s "$sbom_path" ]
  printf '%s' '[{"verificationResult":{"statement":{"predicate":'
  cat "$sbom_path"
  printf '%s\n' '}}}]'
fi
EOF

chmod +x "$tmp/bin/cosign" "$tmp/bin/gh"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

write_bundle() {
  file=$1
  subject=$2
  predicate=$3
  artifact_sha=$4
  identity=$5
  issuer=$6
  repository=$7
  workflow=$8
  source_ref=$9
  source_digest=${10}
  sbom_path=${11:-}
  {
    echo "subject=$subject"
    echo "predicate=$predicate"
    echo "artifact_sha=$artifact_sha"
    echo "identity=$identity"
    echo "issuer=$issuer"
    echo "repository=$repository"
    echo "workflow=$workflow"
    echo "source_ref=$source_ref"
    echo "source_digest=$source_digest"
    echo "sbom_path=$sbom_path"
  } > "$file"
}

write_checksums() {
  dir=$1
  chart=$2
  (
    cd "$dir"
    for file in "$chart" "$chart.sigstore.json" image.spdx.json chart.spdx.json \
      image.provenance.sigstore.json chart.provenance.sigstore.json \
      image.sbom.sigstore.json chart.sbom.sigstore.json IMAGE_REF IMAGE_REPOSITORY SOURCE_COMMIT; do
      digest=$(sha256_file "$file")
      printf '%s  %s\n' "$digest" "$file"
    done > SHA256SUMS
  )
}

make_fixture() {
  dir=$1
  repository=acme/fortigate-external-dns
  tag=v1.2.3
  digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  image_repository=ghcr.io/acme/fortigate-external-dns
  image_ref="${image_repository}@sha256:${digest}"
  source_commit=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  identity="https://github.com/${repository}/.github/workflows/release.yml@refs/tags/${tag}"
  issuer=https://token.actions.githubusercontent.com
  workflow="${repository}/.github/workflows/release.yml"
  chart=fortigate-external-dns-1.2.3.tgz
  mkdir -p "$dir"
  printf 'chart archive bytes\n' > "$dir/$chart"
  printf '{"spdxVersion":"SPDX-2.3","packages":[]}\n' > "$dir/image.spdx.json"
  printf '{"spdxVersion":"SPDX-2.3","packages":[]}\n' > "$dir/chart.spdx.json"
  cp "$dir/image.spdx.json" "$dir/.attested-image.spdx.json"
  cp "$dir/chart.spdx.json" "$dir/.attested-chart.spdx.json"
  printf '%s\n' "$image_ref" > "$dir/IMAGE_REF"
  printf '%s\n' "$image_repository" > "$dir/IMAGE_REPOSITORY"
  printf '%s\n' "$source_commit" > "$dir/SOURCE_COMMIT"
  chart_sha=$(sha256_file "$dir/$chart")
  write_bundle "$dir/$chart.sigstore.json" "$dir/$chart" signature "$chart_sha" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit"
  write_bundle "$dir/image.signature" "$image_ref" signature "$digest" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit"
  write_bundle "$dir/image.provenance.sigstore.json" "oci://$image_ref" "https://slsa.dev/provenance/v1" "$digest" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit"
  write_bundle "$dir/chart.provenance.sigstore.json" "$dir/$chart" "https://slsa.dev/provenance/v1" "$chart_sha" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit"
  write_bundle "$dir/image.sbom.sigstore.json" "oci://$image_ref" "https://spdx.dev/Document/v2.3" "$digest" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit" "$dir/.attested-image.spdx.json"
  write_bundle "$dir/chart.sbom.sigstore.json" "$dir/$chart" "https://spdx.dev/Document/v2.3" "$chart_sha" \
    "$identity" "$issuer" "$repository" "$workflow" "refs/tags/$tag" "$source_commit" "$dir/.attested-chart.spdx.json"
  write_checksums "$dir" "$chart"
}

run_verify() {
  dir=$1
  repository=${2:-acme/fortigate-external-dns}
  image_ref=${3:-$(cat "$dir/IMAGE_REF")}
  PATH="$tmp/bin:$PATH" \
    JQ_BIN="$(command -v jq)" \
    COSIGN_BIN="$tmp/bin/cosign" \
    GH_BIN="$tmp/bin/gh" \
    FAKE_IMAGE_SIGNATURE="$dir/image.signature" \
    sh "$verifier" "$repository" v1.2.3 "$image_ref" "$dir/fortigate-external-dns-1.2.3.tgz" "$dir"
}

expect_failure() {
  description=$1
  shift
  if "$@" >/dev/null 2>&1; then
    echo "expected release verification to reject: $description" >&2
    exit 1
  fi
}

make_fixture "$tmp/valid"
run_verify "$tmp/valid" >/dev/null

make_fixture "$tmp/modified"
printf 'modified\n' >> "$tmp/modified/fortigate-external-dns-1.2.3.tgz"
expect_failure "modified chart bytes" run_verify "$tmp/modified"

make_fixture "$tmp/sbom"
printf '{"spdxVersion":"SPDX-2.3","packages":[{"name":"tampered"}]}\n' > "$tmp/sbom/image.spdx.json"
write_checksums "$tmp/sbom" fortigate-external-dns-1.2.3.tgz
expect_failure "SBOM bytes that do not match the signed predicate" run_verify "$tmp/sbom"

make_fixture "$tmp/digest"
wrong_digest=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
wrong_ref="ghcr.io/acme/fortigate-external-dns@sha256:$wrong_digest"
printf '%s\n' "$wrong_ref" > "$tmp/digest/IMAGE_REF"
write_checksums "$tmp/digest" fortigate-external-dns-1.2.3.tgz
expect_failure "wrong image digest" run_verify "$tmp/digest" acme/fortigate-external-dns "$wrong_ref"

make_fixture "$tmp/identity"
sed 's#^identity=.*#identity=https://github.com/acme/other/.github/workflows/release.yml@refs/tags/v1.2.3#' \
  "$tmp/identity/image.signature" > "$tmp/identity/image.signature.bad"
mv "$tmp/identity/image.signature.bad" "$tmp/identity/image.signature"
expect_failure "wrong signing identity" run_verify "$tmp/identity"

make_fixture "$tmp/issuer"
sed 's#^issuer=.*#issuer=https://issuer.example.invalid#' \
  "$tmp/issuer/image.signature" > "$tmp/issuer/image.signature.bad"
mv "$tmp/issuer/image.signature.bad" "$tmp/issuer/image.signature"
expect_failure "wrong OIDC issuer" run_verify "$tmp/issuer"

make_fixture "$tmp/repository"
expect_failure "wrong source repository" run_verify "$tmp/repository" acme/other

echo "release artifact verification positive and negative checks passed"
