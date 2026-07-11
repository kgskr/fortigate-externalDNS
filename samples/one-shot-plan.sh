#!/usr/bin/env sh
# Current executable approval flow. Run from a checkout and provide the same
# ordinary discovery/FortiGate flags to both commands.
set -eu

: "${FORTIGATE_API_TOKEN:?set FORTIGATE_API_TOKEN from a secure source}"
: "${FORTIGATE_URL:?set FORTIGATE_URL}"
: "${FORTIGATE_ZONE:?set FORTIGATE_ZONE}"

PLAN_FILE=${PLAN_FILE:-fortigate-plan.json}

go run ./cmd/fortigate-external-dns \
  --once --dry-run --plan-output="$PLAN_FILE" \
  --source=service --domain-filter=example.com \
  --fortigate-url="$FORTIGATE_URL" --fortigate-zone="$FORTIGATE_ZONE"

PLAN_HASH=$(shasum -a 256 "$PLAN_FILE" | awk '{print $1}')
printf 'Review %s, then run the approval command with hash %s\n' "$PLAN_FILE" "$PLAN_HASH"

# Approval is a separate invocation so discovery and provider preconditions are
# revalidated immediately before apply. Remove --dry-run only when writes are
# intended and supply all the same scope/safety flags used above.
go run ./cmd/fortigate-external-dns \
  --once --dry-run --approved-plan-hash="$PLAN_HASH" \
  --source=service --domain-filter=example.com \
  --fortigate-url="$FORTIGATE_URL" --fortigate-zone="$FORTIGATE_ZONE"
