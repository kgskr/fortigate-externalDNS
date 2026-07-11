#!/usr/bin/env sh
set -eu

: "${REPOSITORY:?set REPOSITORY, for example kgskr/fortigate-externalDNS}"
: "${TAG:?set a published v* TAG}"

EVIDENCE_DIR=${EVIDENCE_DIR:-release-evidence}
mkdir -p "$EVIDENCE_DIR"
gh release download "$TAG" --repo "$REPOSITORY" --dir "$EVIDENCE_DIR"
IMAGE_REF=$(cat "$EVIDENCE_DIR/IMAGE_REF")
CHART="$EVIDENCE_DIR/fortigate-external-dns-${TAG#v}.tgz"

scripts/verify-release-artifacts.sh \
  "$REPOSITORY" "$TAG" "$IMAGE_REF" "$CHART" "$EVIDENCE_DIR"
