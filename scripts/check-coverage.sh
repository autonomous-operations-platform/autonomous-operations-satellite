#!/usr/bin/env bash
set -euo pipefail

PROFILE="${1:?usage: check-coverage.sh <profile> <threshold>}"
THRESHOLD="${2:?usage: check-coverage.sh <profile> <threshold>}"

# Exclude from the coverage gate code that is not meaningfully unit-testable:
#   *.pb.go   generated protobuf/gRPC code; not hand-written
#   /cmd/     binary entrypoints; process wiring (sockets, signals, database
#             connections) is exercised by integration tests, not unit tests
# Behavior of the generated and entrypoint code is still validated by tests;
# only its statements are kept out of the ratio.
FILTERED="$(mktemp)"
trap 'rm -f "$FILTERED"' EXIT
grep -vE '\.pb\.go:|/cmd/' "$PROFILE" >"$FILTERED" || true

coverage=$(go tool cover -func="$FILTERED" | awk '/^total/{print $3+0}')

echo "Coverage: ${coverage}%"

if awk "BEGIN {exit !(${coverage} < ${THRESHOLD})}"; then
  echo "FAIL: coverage ${coverage}% is below threshold ${THRESHOLD}%"
  exit 1
fi
