# satellite-systemd.nix — portable systemd integration for non-NixOS distros.
#
# The NixOS modules (module.nix, doris-module.nix) only run under nixos-rebuild. This
# package makes the SAME stack — the AOS satellite plus its Apache Doris FE+BE — run on any
# systemd Linux that has Nix (Ubuntu, Debian, Fedora, RHEL, …). It generates four plain
# systemd unit files plus a `aos-ctl.sh install/uninstall` script that symlinks them into
# place. The Doris runtime logic is shared with the NixOS module via doris-scripts.nix, so
# the hard-won lessons (writable DORIS_HOME, routable priority_networks, AF_NETLINK/
# getifaddrs, NSS resolution, BE registration) live in exactly one place.
#
# Units generated:
#   doris-fe.service / doris-be.service / doris-register-be.service — the datastore.
#   aos-satellite.service — the satellite, ordered AFTER doris-register-be so a single
#     `systemctl start` brings the whole stack up in order. Doris is a SERVICE dependency
#     (systemd ordering), NOT a Nix-package dependency: the satellite is a static binary
#     that talks to Doris over TCP, so its closure stays just itself. The satellite's own
#     DB-retry loop (+ Restart=on-failure) remains the real readiness gate.
#
# The generated units carry absolute /nix/store paths (the satellite + Doris packages, JDK
# home, glibc, the wrapped scripts). They are therefore HOST-SPECIFIC: regenerate with
# `nix build` per machine rather than committing them.
#
# Install (needs systemd + root):   sudo ./result/aos-ctl.sh install
# Remove:                           sudo ./result/aos-ctl.sh uninstall
#
# `install` SYMLINKS (not copies) the units into /etc/systemd/system, so a later
# `nix build` + reload refreshes the live units in place.

{ pkgs
, satellite ? (import ./satellite-package.nix {
    inherit pkgs;
    ctfVerified = import ./satellite-ctf.nix { inherit pkgs; };
  })
, doris ? (import ./doris.nix { inherit pkgs; })
, jdk ? pkgs.jdk17
, user ? "aos-satellite"
, group ? "aos-satellite"
, feHeap ? "2048m"
, beMemLimit ? "4G"
, priorityNetworks ? "auto"
}:

let
  inherit (pkgs) lib;

  scripts = import ./doris-scripts.nix { inherit pkgs; };

  # Upstream start_*/stop_* scripts shell out to these; bake them onto the units' PATH
  # (Environment=PATH) since a non-NixOS host won't have them where the scripts expect.
  unitPath = lib.makeBinPath [
    pkgs.bash pkgs.coreutils pkgs.util-linux pkgs.gnused pkgs.gnugrep
    pkgs.gawk pkgs.procps pkgs.iproute2
  ];

  javaHome = jdk.home;        # …/lib/openjdk — bin/java + lib/server/libjvm.so
  glibcLib = "${pkgs.glibc}/lib";

  # Doris BE loads the system timezone database at startup (TimezoneUtils::
  # load_timezones_to_cache); on a minimal/NixOS host there is no /usr/share/zoneinfo, so
  # BE aborts with "[E-7412] Cannot find system tzfile". Point TZDIR at Nix's tzdata.
  tzdir = "${pkgs.tzdata}/share/zoneinfo";

  stateBase = "/var/lib/doris";

  # ── preStart helpers, mirroring the NixOS module's preStart exactly ──────────
  # Seed the writable DORIS_HOME, lay down conf, set priority_networks. The NixOS module
  # does this inline in preStart; here it is an ExecStartPre script per unit. Same calls
  # to the same wrapped scripts.
  fePreStart = pkgs.writeShellScript "doris-fe-prestart" ''
    set -euo pipefail
    STATE=${stateBase}/fe
    mkdir -p "$STATE"
    STATE="$STATE" DORIS_PKG=${doris} ${scripts.seed-home}/bin/doris-seed-home.sh fe doris-meta
    [ -L "$STATE"/doris-meta ] && rm -f "$STATE"/doris-meta || true
    mkdir -p "$STATE"/doris-meta
    if [ ! -f "$STATE"/conf/fe.conf ]; then
      cp ${doris}/fe/conf/fe.conf "$STATE"/conf/fe.conf
      chmod u+w "$STATE"/conf/fe.conf
      sed -i \
        -e 's|^#\?meta_dir.*|meta_dir = ${stateBase}/fe/doris-meta|' \
        -e 's|^#\?sys_log_dir.*|sys_log_dir = ${stateBase}/fe/log|' \
        -e 's|-Xmx[0-9]\+[kmgKMG]|-Xmx${feHeap}|g' \
        -e 's|-Xms[0-9]\+[kmgKMG]|-Xms${feHeap}|g' \
        "$STATE"/conf/fe.conf
      grep -qE '^#?[[:space:]]*default_replication_num[[:space:]]*=' "$STATE"/conf/fe.conf \
        || printf 'default_replication_num = 1\n' >> "$STATE"/conf/fe.conf
      CONF="$STATE"/conf/fe.conf
      eval "$(PRIORITY_NETWORKS=${lib.escapeShellArg priorityNetworks} ${scripts.derive-netaddr}/bin/doris-derive-netaddr.sh)"
      ${scripts.set-priority-networks}/bin/doris-set-priority-networks.sh "$CONF" "$PN"
    fi
  '';

  bePreStart = pkgs.writeShellScript "doris-be-prestart" ''
    set -euo pipefail
    STATE=${stateBase}/be
    mkdir -p "$STATE"
    STATE="$STATE" DORIS_PKG=${doris} ${scripts.seed-home}/bin/doris-seed-home.sh be storage
    [ -L "$STATE"/storage ] && rm -f "$STATE"/storage || true
    mkdir -p "$STATE"/storage
    if [ ! -f "$STATE"/conf/be.conf ]; then
      cp ${doris}/be/conf/be.conf "$STATE"/conf/be.conf
      chmod u+w "$STATE"/conf/be.conf
      sed -i \
        -e 's|^#\?storage_root_path.*|storage_root_path = ${stateBase}/be/storage|' \
        -e 's|^#\?sys_log_dir.*|sys_log_dir = ${stateBase}/be/log|' \
        "$STATE"/conf/be.conf
      if grep -qE '^#?[[:space:]]*mem_limit' "$STATE"/conf/be.conf; then
        sed -i 's|^#\?[[:space:]]*mem_limit.*|mem_limit = ${beMemLimit}|' "$STATE"/conf/be.conf
      else
        printf 'mem_limit = %s\n' '${beMemLimit}' >> "$STATE"/conf/be.conf
      fi
      CONF="$STATE"/conf/be.conf
      eval "$(PRIORITY_NETWORKS=${lib.escapeShellArg priorityNetworks} ${scripts.derive-netaddr}/bin/doris-derive-netaddr.sh)"
      ${scripts.set-priority-networks}/bin/doris-set-priority-networks.sh "$CONF" "$PN"
    fi
  '';

  registerScript = pkgs.writeShellScript "doris-register" ''
    set -euo pipefail
    PRIORITY_NETWORKS=${lib.escapeShellArg priorityNetworks} \
    MYSQL_BIN=mysql FE_HOST=127.0.0.1 FE_PORT=9030 \
      ${scripts.register-be}/bin/doris-register-be.sh
  '';

  # ── Unit files ───────────────────────────────────────────────────────────────
  # Hardening mirrors the NixOS module's dorisHardening, including AF_NETLINK (BE's
  # getifaddrs()) and LimitNOFILE. vm.max_map_count is a host-kernel setting, delivered
  # via the sysctl.d drop-in below (BE's preflight needs >= 2000000).
  commonHardening = ''
    LimitNOFILE=655360
    NoNewPrivileges=true
    ProtectSystem=full
    ProtectHome=true
    PrivateTmp=true
    PrivateDevices=true
    ProtectKernelTunables=true
    ProtectKernelModules=true
    ProtectKernelLogs=true
    ProtectControlGroups=true
    RestrictRealtime=true
    RestrictSUIDSGID=true
    LockPersonality=true
    CapabilityBoundingSet=
    AmbientCapabilities=
    RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
  '';

  feService = ''
    [Unit]
    Description=Apache Doris Frontend
    After=network-online.target
    Wants=network-online.target

    [Service]
    Type=forking
    User=${user}
    Group=${group}
    Environment=JAVA_HOME=${javaHome}
    Environment=DORIS_HOME=${stateBase}/fe
    Environment=TZDIR=${tzdir}
    Environment=PATH=${unitPath}
    StateDirectory=doris/fe
    StateDirectoryMode=0750
    WorkingDirectory=${stateBase}/fe
    PIDFile=${stateBase}/fe/bin/fe.pid
    ExecStartPre=${fePreStart}
    ExecStart=${stateBase}/fe/bin/start_fe.sh --daemon
    ExecStop=${stateBase}/fe/bin/stop_fe.sh
    Restart=on-failure
    RestartSec=10
    ${commonHardening}
    [Install]
    WantedBy=multi-user.target
  '';

  beService = ''
    [Unit]
    Description=Apache Doris Backend
    After=network-online.target
    Wants=network-online.target

    [Service]
    Type=simple
    User=${user}
    Group=${group}
    Environment=JAVA_HOME=${javaHome}
    Environment=DORIS_HOME=${stateBase}/be
    Environment=LD_LIBRARY_PATH=${glibcLib}
    Environment=TZDIR=${tzdir}
    Environment=PATH=${unitPath}
    StateDirectory=doris/be
    StateDirectoryMode=0750
    WorkingDirectory=${stateBase}/be
    ExecStartPre=${bePreStart}
    ExecStart=${stateBase}/be/bin/start_be.sh --console
    Restart=on-failure
    RestartSec=10
    ${commonHardening}
    [Install]
    WantedBy=multi-user.target
  '';

  registerBeService = ''
    [Unit]
    Description=Register the Doris BE with the FE (one-time cluster bootstrap)
    After=doris-fe.service doris-be.service network-online.target
    Wants=doris-fe.service doris-be.service network-online.target

    [Service]
    Type=oneshot
    RemainAfterExit=true
    User=${user}
    Group=${group}
    ExecStart=${registerScript}

    [Install]
    WantedBy=multi-user.target
  '';

  # The satellite itself. Ordered AFTER the Doris bootstrap so a single `systemctl start`
  # brings the stack up in order; the satellite's own DB-retry loop + Restart=on-failure
  # are the real readiness gate (Doris is a service dependency, not a hard precondition).
  # Hardening mirrors the NixOS module (nix/module.nix): dedicated user, full sandbox,
  # seccomp allowlist, no capabilities. The satellite is a static binary with no runtime
  # deps, so no JAVA_HOME/LD_LIBRARY_PATH/TZDIR is needed here.
  satelliteService = ''
    [Unit]
    Description=AOS satellite
    After=doris-register-be.service network-online.target
    Wants=doris-register-be.service network-online.target

    [Service]
    ExecStart=${lib.getExe satellite}
    Restart=on-failure
    RestartSec=5
    User=${user}
    Group=${group}

    NoNewPrivileges=true
    ProtectSystem=strict
    ProtectHome=true
    PrivateTmp=true
    PrivateDevices=true
    ProtectKernelTunables=true
    ProtectKernelModules=true
    ProtectKernelLogs=true
    ProtectControlGroups=true
    ProtectClock=true
    ProtectHostname=true
    ProtectProc=invisible
    ProcSubset=pid
    RestrictNamespaces=true
    RestrictRealtime=true
    RestrictSUIDSGID=true
    LockPersonality=true
    MemoryDenyWriteExecute=true
    RemoveIPC=true
    SystemCallFilter=@system-service ~@privileged ~@resources
    SystemCallArchitectures=native
    CapabilityBoundingSet=
    AmbientCapabilities=
    RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
    RuntimeDirectory=aos-satellite
    StateDirectory=aos-satellite
    WorkingDirectory=%S/aos-satellite
    UMask=0077

    [Install]
    WantedBy=multi-user.target
  '';

  sysctlConf = ''
    # Apache Doris BE memory-maps a very large number of segment/index files and refuses
    # to start unless vm.max_map_count >= 2000000.
    vm.max_map_count = 2000000
  '';

  sysusersConf = ''
    # Create the shared Doris/satellite service user+group.
    u ${user} - "AOS satellite / Doris service user" ${stateBase}
  '';

  # install/uninstall as a plain bash script (no `make` dependency). It resolves its own
  # directory ($SELF) so the unit/drop-in symlinks point back into this package output.
  # Needs systemd + root; uses system tools (systemctl, useradd, sysctl) from PATH.
  ctlScript = ''
    # aos-ctl.sh — install/uninstall the AOS satellite + Doris systemd units by SYMLINK.
    #
    #   sudo ./aos-ctl.sh install      symlink units + drop-ins, create user, enable --now
    #   sudo ./aos-ctl.sh uninstall    disable + remove the symlinks
    #
    # Units carry absolute /nix/store paths; re-run `nix build` then `install` to refresh
    # them after a package change (symlinks pick up the new output automatically if you
    # point at the new result/).

    # Directory this script lives in (the package output root).
    SELF="$(cd "$(dirname "''${BASH_SOURCE[0]}")" && pwd)"

    UNIT_DIR=/etc/systemd/system
    SYSCTL_DIR=/etc/sysctl.d
    SYSUSERS_DIR=/etc/sysusers.d
    # Order matters for enable --now: datastore first, satellite last.
    UNITS="doris-fe.service doris-be.service doris-register-be.service aos-satellite.service"
    DORIS_USER=${lib.escapeShellArg user}
    DORIS_GROUP=${lib.escapeShellArg group}
    STATE=${lib.escapeShellArg stateBase}

    install_units() {
      echo ">>> creating drop-in directories"
      mkdir -p "$SYSCTL_DIR" "$SYSUSERS_DIR" "$UNIT_DIR"

      echo ">>> creating user $DORIS_USER if missing"
      if command -v systemd-sysusers >/dev/null 2>&1; then
        ln -sfn "$SELF/sysusers.d/aos-satellite.conf" "$SYSUSERS_DIR/aos-satellite.conf"
        systemd-sysusers "$SELF/sysusers.d/aos-satellite.conf" || true
      fi
      getent group  "$DORIS_GROUP" >/dev/null || groupadd --system "$DORIS_GROUP"
      getent passwd "$DORIS_USER"  >/dev/null || useradd --system -g "$DORIS_GROUP" \
        -d "$STATE" -s /usr/sbin/nologin "$DORIS_USER"

      echo ">>> state dir $STATE"
      install -d -o "$DORIS_USER" -g "$DORIS_GROUP" -m 0750 "$STATE"

      echo ">>> sysctl drop-in (vm.max_map_count)"
      ln -sfn "$SELF/sysctl.d/99-doris.conf" "$SYSCTL_DIR/99-doris.conf"
      sysctl --system || true

      echo ">>> symlinking units into $UNIT_DIR"
      for u in $UNITS; do ln -sfn "$SELF/systemd/$u" "$UNIT_DIR/$u"; done
      systemctl daemon-reload
      # shellcheck disable=SC2086  # $UNITS is an intentional space-separated list
      systemctl enable --now $UNITS
      echo ">>> done. Check: systemctl status doris-fe doris-be aos-satellite"
    }

    uninstall_units() {
      # shellcheck disable=SC2086
      systemctl disable --now $UNITS || true
      for u in $UNITS; do rm -f "$UNIT_DIR/$u"; done
      rm -f "$SYSCTL_DIR/99-doris.conf" "$SYSUSERS_DIR/aos-satellite.conf"
      systemctl daemon-reload
      echo ">>> removed. (The $DORIS_USER user and $STATE are left in place.)"
    }

    case "''${1:-}" in
      install)   install_units ;;
      uninstall) uninstall_units ;;
      *) echo "usage: $0 {install|uninstall}" >&2; exit 2 ;;
    esac
  '';
in
pkgs.runCommand "satellite-systemd"
  {
    meta = with lib; {
      description = "Portable systemd units + aos-ctl.sh installer for the AOS satellite + Apache Doris FE/BE";
      platforms = [ "x86_64-linux" "aarch64-linux" ];
    };
  }
  ''
    mkdir -p "$out/systemd" "$out/sysctl.d" "$out/sysusers.d"
    cp ${pkgs.writeText "doris-fe.service" feService}            "$out/systemd/doris-fe.service"
    cp ${pkgs.writeText "doris-be.service" beService}            "$out/systemd/doris-be.service"
    cp ${pkgs.writeText "doris-register-be.service" registerBeService} "$out/systemd/doris-register-be.service"
    cp ${pkgs.writeText "aos-satellite.service" satelliteService} "$out/systemd/aos-satellite.service"
    cp ${pkgs.writeText "99-doris.conf" sysctlConf}              "$out/sysctl.d/99-doris.conf"
    cp ${pkgs.writeText "aos-satellite.conf" sysusersConf}       "$out/sysusers.d/aos-satellite.conf"
    install -m0755 ${pkgs.writeShellApplication { name = "aos-ctl.sh"; text = ctlScript; }}/bin/aos-ctl.sh "$out/aos-ctl.sh"
  ''
