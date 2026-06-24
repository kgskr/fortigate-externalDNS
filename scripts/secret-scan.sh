#!/usr/bin/env sh
# Lightweight scan for accidentally committed FortiGate API tokens. Looks only at
# git-tracked files and ignores documented placeholders.
set -eu

# Match an API token environment assignment or flag with a concrete value, while
# allowing obvious placeholders used throughout the docs and samples.
pattern='(FORTIGATE_API_TOKEN|--fortigate-api-token)[=: ]+[A-Za-z0-9._-]{12,}'

matches=$(git grep -nIE "$pattern" -- . ':!scripts/secret-scan.sh' 2>/dev/null \
  | grep -ivE 'placeholder|api-token-from-kubernetes-secret|<.*>|unit-test-credential|example' || true)

if [ -n "$matches" ]; then
  echo "secret-scan: possible committed API token:"
  echo "$matches"
  exit 1
fi

echo "secret-scan: no committed API tokens detected (ok)"
