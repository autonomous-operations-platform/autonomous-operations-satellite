#!/usr/bin/env bash
# doris-seed-home.sh — build a WRITABLE DORIS_HOME tree from the read-only Nix store.
#
# The upstream start_fe.sh / start_be.sh resolve DORIS_HOME to the parent of their own
# bin/ directory (`curdir="$(cd "$(dirname "$0")" && pwd)"; DORIS_HOME="$(cd
# "${curdir}/.." && pwd)"`) and then write the daemon redirect to
# ${DORIS_HOME}/log/{fe,be}.out UNCONDITIONALLY. If DORIS_HOME is the Nix store package
# (immutable, read-only), that write fails with "Read-only file system". So the scripts
# cannot run in-place from the store.
#
# Fix: build a writable per-service tree (the caller's $STATE, e.g. /var/lib/doris/fe)
# and run the scripts from there, so DORIS_HOME resolves to the writable copy and log/,
# conf/, data and the *.out redirect all land on a writable filesystem.
#
# To keep the tree cheap (FE's lib/ is hundreds of MB of jars) we do NOT copy the
# package. Instead:
#   bin/   — a REAL directory of symlinks to the store's scripts. It MUST be a real dir,
#            not a symlink: the script does `cd "$(dirname "$0")" && pwd`, which resolves
#            symlinks, so a symlinked bin/ would make DORIS_HOME resolve back into the
#            store. Real dir → pwd stays under $STATE and DORIS_HOME is the writable tree.
#   lib/   — a symlink to the store (referenced only as ${DORIS_HOME}/lib, never cd-ed
#            into, so a symlink is fine and costs nothing).
#   conf/  — a REAL dir seeded with a WRITABLE copy of the ENTIRE store conf/ EXCEPT the
#            main .conf (fe.conf/be.conf are owned/edited by the caller's preStart). The
#            sidecar files (ldap.conf, ssl/, suppressions, odbcinst.ini, …) must be
#            present and writable — a symlink or partial copy would lose them.
#   log/, data — real writable directories, preserved across restarts.
#
# The mutable DATA dirs (fe/doris-meta, be/storage) are shipped in the package but MUST
# NOT be symlinked to the read-only store: Doris writes into them at runtime. The symlink
# loop skips them (via the $DATA_DIRS case pattern) so the real writable dirs the caller
# creates survive. See memory: doris-seed-mutable-dirs (EROFS on package data dirs).
#
# Usage:
#   STATE=/var/lib/doris/fe DORIS_PKG=/nix/store/…-apache-doris \
#     doris-seed-home.sh fe doris-meta
#
#   $1         component — "fe" or "be"
#   $2         data-dir case pattern of the component's MUTABLE dirs (e.g. "doris-meta",
#              "storage"; join multiple with "|")
#   $STATE     writable per-service home to build (required)
#   $DORIS_PKG the Doris package root containing fe/ and be/ (required)
set -euo pipefail

component="${1:?usage: doris-seed-home.sh <component> <data-dirs>}"
dataDirs="${2:?usage: doris-seed-home.sh <component> <data-dirs>}"
: "${STATE:?STATE (writable DORIS_HOME) must be set}"
: "${DORIS_PKG:?DORIS_PKG (Doris package root) must be set}"

mkdir -p "$STATE"/log "$STATE"/conf

# bin/: real dir of symlinks to the store's scripts (refreshed each start).
rm -rf "${STATE:?}"/bin
mkdir -p "$STATE"/bin
for f in "$DORIS_PKG/$component"/bin/*; do
  ln -sfn "$f" "$STATE"/bin/"$(basename "$f")"
done

# conf/: writable copy of every file EXCEPT the main .conf (owned by the caller).
# Refresh sidecar config (ldap.conf, ssl/, suppressions, …) from the store.
for p in "$DORIS_PKG/$component"/conf/*; do
  name="$(basename "$p")"
  case "$name" in
    fe.conf|be.conf) ;;  # the caller seeds + edits these, then preserves them
    *)
      rm -rf "$STATE"/conf/"$name"
      cp -aL --no-preserve=mode "$p" "$STATE"/conf/"$name"
      ;;
  esac
done

# Everything else immutable (lib, plugins, …) is symlinked straight to the store —
# except bin/log/conf (handled above) and the mutable data dirs, which stay as the real
# writable dirs the caller creates.
for p in "$DORIS_PKG/$component"/*; do
  name="$(basename "$p")"
  # $dataDirs is a deliberate case pattern (e.g. "storage" or "a|b"); the glob/alternation
  # is intended, so quoting it would break the match. SC2254 is expected here.
  # shellcheck disable=SC2254
  case "$name" in
    bin|log|conf|$dataDirs) ;;
    *) ln -sfn "$p" "$STATE"/"$name" ;;
  esac
done
