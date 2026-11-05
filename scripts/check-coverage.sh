#!/usr/bin/env bash
set -euo pipefail

PROFILE="${1:?usage: check-coverage.sh <profile> <threshold>}"
THRESHOLD="${2:?usage: check-coverage.sh <profile> <threshold>}"

coverage=$(go tool cover -func="$PROFILE" | awk '/^total/{print $3+0}')

echo "Coverage: ${coverage}%"

if awk "BEGIN {exit !(${coverage} < ${THRESHOLD})}"; then
  echo "FAIL: coverage ${coverage}% is below threshold ${THRESHOLD}%"
  exit 1
fi
