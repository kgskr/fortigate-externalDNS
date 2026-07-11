#!/bin/sh
set -eu

release_workflow=${RELEASE_WORKFLOW:-.github/workflows/release.yml}
ci_workflow=${CI_WORKFLOW:-.github/workflows/ci.yml}
verify_script=${VERIFY_SCRIPT:-scripts/verify-release-artifacts.sh}

fail() {
  echo "release workflow validation failed: $*" >&2
  exit 1
}

for file in "$release_workflow" "$ci_workflow" "$verify_script"; do
  [ -s "$file" ] || fail "missing $file"
done

# Parse both workflows before checking their security policy. Ruby/Psych is
# available on GitHub-hosted Ubuntu and macOS runners and catches malformed YAML.
ruby -e 'require "yaml"; ARGV.each { |path| YAML.safe_load(File.read(path), aliases: true) }' \
  "$release_workflow" "$ci_workflow" || fail "invalid workflow YAML"

grep -Eq '^  release:$' "$release_workflow" || fail "release trigger is missing"
grep -Eq '^    types: \[published\]$' "$release_workflow" || fail "release trigger is not published-only"
if grep -Eq '^  (pull_request|push|workflow_dispatch|pull_request_target):' "$release_workflow"; then
  fail "publishing workflow has an untrusted trigger"
fi

id_token_count=$(grep -Ec '^      id-token: write$' "$release_workflow" || true)
[ "$id_token_count" -eq 1 ] || fail "id-token: write must appear exactly once"
grep -Eq '^  publish:$' "$release_workflow" || fail "publish job is missing"
grep -Fq "github.event.action == 'published'" "$release_workflow" || fail "publish job lacks the published-event guard"
grep -Eq '^      contents: write$' "$release_workflow" || fail "publish job cannot attach release evidence"
grep -Eq '^      packages: write$' "$release_workflow" || fail "publish job cannot publish immutable artifacts"
grep -Eq '^      attestations: write$' "$release_workflow" || fail "publish job cannot persist attestations"
grep -Eq '^      artifact-metadata: write$' "$release_workflow" || fail "publish job cannot associate image evidence"

if grep -Eq '(^|[[:space:]])(id-token|packages|attestations|artifact-metadata): write' "$ci_workflow"; then
  fail "PR/reusable CI has publishing or signing authority"
fi
grep -Eq '^permissions:$' "$ci_workflow" || fail "CI must declare permissions"
grep -Eq '^  contents: read$' "$ci_workflow" || fail "CI must remain read-only"
grep -Fq 'scripts/release-workflow-check.sh' "$ci_workflow" || fail "CI does not run the release workflow check"

uses_lines=$(grep -E '^[[:space:]]*-?[[:space:]]*uses:' "$release_workflow" || true)
[ -n "$uses_lines" ] || fail "release workflow has no actions"
if printf '%s\n' "$uses_lines" | grep -Ev 'uses:[[:space:]]+\./|@[0-9a-f]{40}([[:space:]]+#.*)?$' >/dev/null; then
  fail "every release action must be pinned to a full commit SHA"
fi

grep -Fq 'sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6' "$release_workflow" || fail "Cosign installer pin changed"
grep -Fq 'cosign-release: v3.0.6' "$release_workflow" || fail "Cosign version is not pinned"
grep -Fq 'anchore/sbom-action@e22c389904149dbc22b58101806040fa8d37a610' "$release_workflow" || fail "SBOM action pin changed"
grep -Fq 'syft-version: v1.42.3' "$release_workflow" || fail "Syft version is not pinned"
grep -Fq 'actions/attest@a1948c3f048ba23858d222213b7c278aabede763' "$release_workflow" || fail "attestation action pin changed"

grep -Fq 'cosign sign --yes "$IMAGE_REF"' "$release_workflow" || fail "immutable image signing is missing"
grep -Fq 'IMAGE_REF: ${{ steps.release.outputs.image }}@${{ steps.image.outputs.digest }}' "$release_workflow" || fail "image signature is not digest-bound"
grep -Fq 'cosign sign-blob --yes --bundle' "$release_workflow" || fail "chart archive signing is missing"
grep -Fq 'format: spdx-json' "$release_workflow" || fail "SPDX JSON generation is missing"
[ "$(grep -Fc 'format: spdx-json' "$release_workflow")" -eq 2 ] || fail "both image and chart SPDX SBOMs are required"
[ "$(grep -Fc 'push-to-registry: true' "$release_workflow")" -eq 2 ] || fail "image provenance and SBOM attestations must be digest-associated"
[ "$(grep -Fc 'https://slsa.dev/provenance/v1' "$verify_script")" -ge 2 ] || fail "image and chart provenance verification is missing"
[ "$(grep -Fc 'https://spdx.dev/Document/v2.3' "$verify_script")" -ge 2 ] || fail "image and chart SBOM attestation verification is missing"
grep -Fq 'fail_on_unmatched_files: true' "$release_workflow" || fail "release asset upload is not fail-closed"
grep -Fq 'overwrite_files: false' "$release_workflow" || fail "release evidence must not be silently replaced"

echo "release workflow permissions, pins, evidence, and PR isolation validated"
