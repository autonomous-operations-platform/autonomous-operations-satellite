#!/usr/bin/env bash
# doris-derive-netaddr.sh — derive the node's routable network address, with retry.
#
# Prints two shell assignments to stdout for the caller to capture:
#   IP=<bare address>    e.g. 10.0.2.15 — used to register the BE and advertise RPC
#   PN=<priority_networks CIDR>
#
# Capture with:  eval "$(PRIORITY_NETWORKS=auto doris-derive-netaddr.sh)"
#
# When PRIORITY_NETWORKS is "auto" (default), both come from the default-route interface
# (`ip route get`) so the deployment is portable with no hardcoded IP; PN is that
# interface's CIDR. Otherwise PRIORITY_NETWORKS is used verbatim as PN and IP is the
# local address that falls inside it.
#
# WHY THIS MATTERS (memory: doris-fe-be-loopback-priority-networks): the address MUST be
# routable, never loopback. If the FE registers itself as 127.0.0.1, the BE's internal
# DNSCache (be/src/util/dns_cache.cpp) can't resolve the "127.0.0.1" literal, the BE→FE
# disk/tablet report never connects, and FE sees the backend with zero capacity
# ("Failed to find enough backend").
#
# RETRIES ~60s: at service start the DHCP lease/route may not be up yet. An empty result
# would silently fall back to loopback, so on total failure this exits non-zero rather
# than proceed with a bad address. Requires `ip` (iproute2), `grep`, `awk` on PATH.
set -euo pipefail

_PN_CFG="${PRIORITY_NETWORKS:-auto}"
IP=""
PN=""
for _i in $(seq 1 30); do
  _ip=$(ip -4 -o route get 1.1.1.1 2>/dev/null | grep -oE 'src [0-9.]+' | awk '{print $2}')
  if [ -n "$_ip" ]; then
    _cidr=$(ip -4 -o addr show 2>/dev/null | awk -v ip="$_ip" '$4 ~ "^"ip"/" {print $4}' | head -n1)
    if [ "$_PN_CFG" = auto ]; then
      IP="$_ip"; PN="$_cidr"
    else
      IP="$_ip"; PN="$_PN_CFG"
    fi
  fi
  [ -n "$IP" ] && [ -n "$PN" ] && break
  sleep 2
done
if [ -z "$IP" ] || [ -z "$PN" ]; then
  echo "ERROR: could not derive network address after retries (priorityNetworks=$_PN_CFG); node would fall back to loopback" >&2
  exit 1
fi

# Emit as shell assignments for the caller to eval. Quote values defensively.
printf 'IP=%q\n' "$IP"
printf 'PN=%q\n' "$PN"
