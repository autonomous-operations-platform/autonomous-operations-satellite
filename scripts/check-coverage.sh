#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and Autonomous Operations Platform contributors
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

PROFILE="${1:?usage: check-coverage.sh <profile> <threshold>}"
THRESHOLD="${2:?usage: check-coverage.sh <profile> <threshold>}"

coverage=$(go tool cover -func="$PROFILE" | awk '/^total/{print $3+0}')

echo "Coverage: ${coverage}%"

if awk "BEGIN {exit !(${coverage} < ${THRESHOLD})}"; then
  echo "FAIL: coverage ${coverage}% is below threshold ${THRESHOLD}%"
  exit 1
fi
