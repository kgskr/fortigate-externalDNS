#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
checker="$root/scripts/release-workflow-check.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/release-workflow-check.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
cp "$root/.github/workflows/ci.yml" "$tmp/ci.yml"

run_check() {
  RELEASE_WORKFLOW="$tmp/release.yml" \
    CI_WORKFLOW="$tmp/ci.yml" \
    VERIFY_SCRIPT="$root/scripts/verify-release-artifacts.sh" \
    sh "$checker"
}

expect_failure() {
  description=$1
  if run_check >/dev/null 2>&1; then
    echo "expected release workflow check to reject: $description" >&2
    exit 1
  fi
}

run_check >/dev/null

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
sed 's#actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0#actions/checkout@v7#' \
  "$tmp/release.yml" > "$tmp/release.mutable"
mv "$tmp/release.mutable" "$tmp/release.yml"
expect_failure "mutable action tag"

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
cp "$root/.github/workflows/ci.yml" "$tmp/ci.yml"
awk 'BEGIN { added=0 } { print } !added && /^  contents: read$/ { print "  id-token: write"; added=1 }' \
  "$tmp/ci.yml" > "$tmp/ci.oidc"
mv "$tmp/ci.oidc" "$tmp/ci.yml"
expect_failure "OIDC permission in pull-request CI"

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
cp "$root/.github/workflows/ci.yml" "$tmp/ci.yml"
sed 's/^on:$/on:\
  pull_request:/' "$tmp/release.yml" > "$tmp/release.pr"
mv "$tmp/release.pr" "$tmp/release.yml"
expect_failure "pull_request publishing trigger"

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
sed 's/format: spdx-json/format: cyclonedx-json/g' "$tmp/release.yml" > "$tmp/release.no-spdx"
mv "$tmp/release.no-spdx" "$tmp/release.yml"
expect_failure "missing SPDX JSON evidence"

cp "$root/.github/workflows/release.yml" "$tmp/release.yml"
sed 's/fail_on_unmatched_files: true/fail_on_unmatched_files: false/' \
  "$tmp/release.yml" > "$tmp/release.open"
mv "$tmp/release.open" "$tmp/release.yml"
expect_failure "non-fail-closed asset attachment"

echo "release workflow negative checks passed"
