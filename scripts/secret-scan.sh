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

matches=$(git grep -nIEi -e "$anchored" -e "$secretfield" -- . ':!scripts/secret-scan.sh' 2>/dev/null \
  | awk '
function is_placeholder(value, lower) {
  lower = tolower(value)
  return lower == "placeholder" \
    || lower == "api-token-from-kubernetes-secret" \
    || lower == "unit-test-credential" \
    || lower == "replace" \
    || lower == "changeme" \
    || value ~ /^<[^>]*>$/
}

function has_non_placeholder(line, key_re, value_re, lower, rest, value, key_start, key_len) {
  lower = tolower(line)
  while (match(lower, key_re)) {
    key_start = RSTART
    key_len = RLENGTH
    rest = substr(line, key_start + key_len)
    if (match(rest, value_re)) {
      value = substr(rest, RSTART, RLENGTH)
      if (!is_placeholder(value)) {
        return 1
      }
    }
    line = substr(line, key_start + key_len + 1)
    lower = tolower(line)
  }
  return 0
}

{
  if (has_non_placeholder($0, "fortigate[_-]?api[_-]?token[=:[:space:]][[:space:]\"\047]*", "^[A-Za-z0-9._/+=-]{12,}") \
    || has_non_placeholder($0, "api[_-]?token[=:][[:space:]\"\047]*", "^[A-Za-z0-9+/]{20,}={0,2}")) {
    print
  }
}
' || true)

if [ -n "$matches" ]; then
  echo "secret-scan: possible committed API token:"
  echo "$matches"
  exit 1
fi

echo "secret-scan: no committed API tokens detected (ok)"
