#!/usr/bin/env sh
# Lightweight scan for accidentally committed FortiGate API tokens. Looks only at
# git-tracked files and ignores documented placeholders. This is a cheap guard,
# not a replacement for a dedicated secret scanner (gitleaks/trufflehog).
set -eu

# Matching is case-insensitive (-i) and tolerant of '-'/'_' key-name variants so
# shapes like FORTIGATE_API_TOKEN, --fortigate-api-token, and fortigate_api_token
# are all caught.
#
# 1) The FortiGate-specific env var or CLI flag, separated by '=', ':', or a space
#    (the space form covers Dockerfile ENV and bare-arg shapes), followed by a
#    token-shaped value of 12+ chars.
anchored="fortigate[_-]?api[_-]?token[=:[:space:]][[:space:]\"']*[A-Za-z0-9._/+=-]{12,}"

# 2) A Kubernetes Secret api-token field whose value is a 20+ char base64-shaped
#    string. Requiring a base64 run (no '.', '(', spaces) keeps this from matching
#    ordinary code such as a Go struct literal `apiToken: cfg.SomeMethod()`.
secretfield="api[_-]?token[=:][[:space:]\"']*[A-Za-z0-9+/]{20,}={0,2}"

# Known documented placeholders. Exclusion is anchored to these specific values so
# a real token is never dropped merely because the line also mentions a common
# word such as "example" or "example.com".
placeholders="placeholder|api-token-from-kubernetes-secret|unit-test-credential|<[^>]*>|REPLACE|CHANGEME|changeme"

matches=$(git grep -nIEi -e "$anchored" -e "$secretfield" -- . ':!scripts/secret-scan.sh' 2>/dev/null \
  | grep -ivE "$placeholders" || true)

if [ -n "$matches" ]; then
  echo "secret-scan: possible committed API token:"
  echo "$matches"
  exit 1
fi

echo "secret-scan: no committed API tokens detected (ok)"
