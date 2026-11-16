#!/usr/bin/env bash
# doris-set-priority-networks.sh — set `priority_networks` in a Doris conf file.
#
# fe.conf and be.conf SHIP a commented `# priority_networks = …` line, so rewrite it in
# place (uncommenting) rather than appending a duplicate.
#
# Usage:
#   doris-set-priority-networks.sh <conf-path> <priority-networks-CIDR>
#
#   $1  path to fe.conf or be.conf (writable)
#   $2  the CIDR to set (the PN value from doris-derive-netaddr.sh)
#
# See memory: doris-fe-be-loopback-priority-networks for why this must be a routable
# CIDR, never loopback.
set -euo pipefail

CONF="${1:?usage: doris-set-priority-networks.sh <conf-path> <cidr>}"
PN="${2:?usage: doris-set-priority-networks.sh <conf-path> <cidr>}"

if grep -qE '^[[:space:]]*#?[[:space:]]*priority_networks[[:space:]]*=' "$CONF"; then
  sed -i "s|^[[:space:]]*#\\?[[:space:]]*priority_networks[[:space:]]*=.*|priority_networks = $PN|" "$CONF"
else
  printf 'priority_networks = %s\n' "$PN" >> "$CONF"
fi
