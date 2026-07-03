#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_root="${TMPDIR:-/tmp}/fortigate-secret-scan-test-$$"
trap 'rm -rf "$tmp_root"' EXIT HUP INT TERM

script_under_test="$repo_root/scripts/secret-scan.sh"
key="FORTIGATE_API_TOKEN"
placeholder_value="api-token-from-kubernetes-secret"
real_token="real.Token_Value-123456"
real_secret_value="QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="

new_repo() {
  case_dir="$tmp_root/$1"
  mkdir -p "$case_dir/scripts"
  cp "$script_under_test" "$case_dir/scripts/secret-scan.sh"
  chmod +x "$case_dir/scripts/secret-scan.sh"
  (
    cd "$case_dir"
    git init -q
    git add scripts/secret-scan.sh
  )
  printf '%s\n' "$case_dir"
}

expect_pass() {
  name="$1"
  shift
  case_dir=$(new_repo "$name")
  (
    cd "$case_dir"
    "$@"
    git add .
    ./scripts/secret-scan.sh >secret-scan.out 2>&1
  )
}

expect_fail() {
  name="$1"
  shift
  case_dir=$(new_repo "$name")
  (
    cd "$case_dir"
    "$@"
    git add .
    if ./scripts/secret-scan.sh >secret-scan.out 2>&1; then
      echo "secret-scan test failed: $name should have detected a token" >&2
      cat secret-scan.out >&2
      exit 1
    fi
  )
}

expect_pass "documented-placeholder" sh -c '
  mkdir -p docs
  printf "%s=%s\n" "$1" "$2" >docs/example.env
' sh "$key" "$placeholder_value"

expect_fail "real-env-token" sh -c '
  mkdir -p docs
  printf "%s=%s\n" "$1" "$2" >docs/leaked.env
' sh "$key" "$real_token"

expect_fail "real-env-token-with-placeholder-on-same-line" sh -c '
  mkdir -p docs
  printf "%s=%s # documented placeholder %s\n" "$1" "$2" "$3" >docs/leaked.env
' sh "$key" "$real_token" "$placeholder_value"

expect_fail "real-secret-field-with-placeholder-on-same-line" sh -c '
  mkdir -p manifests
  printf "api-token: %s # placeholder value %s\n" "$1" "$2" >manifests/secret.yaml
' sh "$real_secret_value" "$placeholder_value"

echo "secret-scan tests passed"
