#!/usr/bin/env sh
# Fails if any GitHub Actions workflow file is present. This repository must not
# enable workflow execution; CI-style validation is run locally via the Makefile.
set -eu

dir=".github/workflows"
if [ ! -d "$dir" ]; then
  echo "no-github-workflows: $dir is absent (ok)"
  exit 0
fi

found=$(find "$dir" -type f \( -name '*.yml' -o -name '*.yaml' \) 2>/dev/null || true)
if [ -n "$found" ]; then
  echo "no-github-workflows: workflow files are not allowed:"
  echo "$found"
  exit 1
fi

echo "no-github-workflows: no workflow files present (ok)"
