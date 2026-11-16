#!/usr/bin/env bash
# doris-register-be.sh — one-time cluster bootstrap: register the BE with the FE.
#
# A BE does not join on its own — the FE must be told about it via `ALTER SYSTEM ADD
# BACKEND`, after which FE starts heartbeating it. Without this, BE sits forever at
# "waiting to receive first heartbeat from frontend" and FE has zero live backends, so
# every storage DDL fails ("System has no available disk"). This forms the single-node
# cluster: derive the node's routable IP (same as the confs — never loopback, which the
# BE's DNSCache can't resolve), wait for FE's query port, then register <IP>:9050 (the BE
# heartbeat_service_port) if not already present. Idempotent.
#
# Usage (env):
#   PRIORITY_NETWORKS=auto         # passed through to doris-derive-netaddr.sh
#   MYSQL_BIN=mysql                # MySQL-protocol client ($out/bin/mysql of mariadb.client)
#   FE_HOST=127.0.0.1              # the mysql CLIENT connects here; 127.0.0.1 is fine —
#   FE_PORT=9030                   # this is NOT Doris's internal DNSCache path
#   doris-register-be.sh
#
# Requires doris-derive-netaddr.sh on PATH (the wrapped script set provides it), plus
# coreutils, grep, iproute2, awk.
set -euo pipefail

MYSQL="${MYSQL_BIN:-mysql}"
FE_HOST="${FE_HOST:-127.0.0.1}"
FE_PORT="${FE_PORT:-9030}"

# The backend MUST be registered by the same routable IP the FE/BE advertise via
# priority_networks — never 127.0.0.1, which the BE's DNSCache can't resolve.
# doris-derive-netaddr.sh prints `IP=…`/`PN=…`; eval captures $IP (same retry/logic the
# confs use).
eval "$(PRIORITY_NETWORKS="${PRIORITY_NETWORKS:-auto}" doris-derive-netaddr.sh)"
BE_HB="$IP:9050"   # BE heartbeat_service_port

# Wait (up to ~5 min) for FE to be ready. The probe is `SHOW BACKENDS`, NOT `SELECT 1`:
# in this Doris version even SELECT 1 needs a scan node and fails with "No backend
# available" until a BE is registered — a chicken-and-egg that would make a SELECT-based
# probe never succeed. SHOW BACKENDS is an admin statement that returns (possibly empty)
# as soon as FE is up.
for _i in $(seq 1 60); do
  if "$MYSQL" -h"$FE_HOST" -P"$FE_PORT" -uroot -e 'SHOW BACKENDS' >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

# Idempotent: only ADD BACKEND if this exact IP isn't already registered.
if "$MYSQL" -h"$FE_HOST" -P"$FE_PORT" -uroot -N -e 'SHOW BACKENDS' 2>/dev/null \
     | grep -qF "$IP"; then
  echo "BE $IP already registered; nothing to do."
  exit 0
fi

echo "Registering backend $BE_HB with FE ..."
"$MYSQL" -h"$FE_HOST" -P"$FE_PORT" -uroot \
  -e "ALTER SYSTEM ADD BACKEND \"$BE_HB\""
