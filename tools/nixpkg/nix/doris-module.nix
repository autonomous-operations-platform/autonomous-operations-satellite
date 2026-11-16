# doris-module.nix — NixOS module for Apache Doris FE + BE
#
# Runs the Frontend (query planner, HTTP/MySQL interface) and Backend (storage
# engine, query execution) as separate systemd services under the same user as
# the AOS satellite (default: aos-satellite), because the satellite and Doris are
# co-located on the same host.
#
# Mutable runtime data (data files, logs, pids) lives in StateDirectory so it
# survives service restarts without needing a world-writable directory.
#
# The JDK is configurable; pass a different package via
# `services.doris.jdk = pkgs.jdk21` if needed. Both FE and BE need it — FE runs on
# the JVM, and BE (a native C++ binary) loads libjvm.so for JNI/Java UDFs.
#
# Hardening: BE is a storage engine that memory-maps data files and forks helper
# processes; a strict seccomp allowlist would break it, so the BE service applies
# lighter-weight hardening (DynamicUser is OFF — shared StateDirectory).

{ config, lib, pkgs, ... }:

let
  cfg = config.services.doris;

  dorisDefault = import ./doris.nix { inherit pkgs; };

  # The Doris runtime logic (seed / derive-netaddr / set-priority-networks / register)
  # lives in ../scripts/*.sh, wrapped by writeShellApplication in doris-scripts.nix.
  # SINGLE SOURCE OF TRUTH: this NixOS module and the portable systemd installer
  # (satellite-systemd.nix) both consume these same wrapped scripts. The wrappers bake the
  # right tools onto each script's PATH (util-linux/getopt, gnused, gnugrep, gawk,
  # procps, iproute2, coreutils; mariadb.client's `mysql` for the register script), so
  # the module no longer needs its own `dorisScriptPath`/`mysqlClient`.
  scripts = import ./doris-scripts.nix { inherit pkgs; };

  # Runtime PATH for the UPSTREAM start_*/stop_* shell scripts (start_fe.sh/start_be.sh).
  # These are written for a conventional Linux distro and shell out to tools NixOS does
  # not put on PATH by default:
  #   util-linux → getopt, swapon        gnused → sed       gnugrep → grep
  #   gawk       → awk                   procps → kill, free, sysctl
  #   coreutils  → uname, date, tr, nohup, cat, wc, chmod, mkdir, env, pwd, echo
  #   iproute2   → ip
  # FE's `java` is invoked via $JAVA_HOME/bin/java (absolute), so the JDK does not need
  # to be on PATH. The macOS-only `brew` branch is never taken on Linux. (The extracted
  # scripts carry their own baked PATH; this list is for the upstream ExecStart scripts.)
  dorisScriptPath = [
    pkgs.bash
    pkgs.coreutils
    pkgs.util-linux
    pkgs.gnused
    pkgs.gnugrep
    pkgs.gawk
    pkgs.procps
    pkgs.iproute2
  ];

  # ── Shell snippet emitters: call the shared wrapped scripts ──────────────────
  # These keep the preStart call-sites identical (`${seedDorisHome "fe" "doris-meta"}`,
  # `${deriveNetAddr}`, `${setPriorityNetworks}`) while the actual logic lives in the
  # single-source-of-truth scripts (../scripts/*.sh via doris-scripts.nix). The full
  # rationale for each — writable DORIS_HOME seeding (the Read-only-FS / EROFS trap),
  # routable-IP derivation (loopback → BE DNSCache failure), and in-place
  # priority_networks rewrite — now lives in those scripts' headers.

  # Build the writable DORIS_HOME tree. `component` is "fe"/"be"; `dataDirs` is the
  # case pattern of the component's MUTABLE dirs to leave as real writable dirs
  # (fe/doris-meta, be/storage). Reads $STATE from the caller's scope.
  seedDorisHome = component: dataDirs: ''
    STATE="$STATE" DORIS_PKG=${cfg.package} \
      ${scripts.seed-home}/bin/doris-seed-home.sh ${component} ${lib.escapeShellArg dataDirs}
  '';

  # Derive the node's routable address and set $IP/$PN in the CALLER's shell scope
  # (preStart reads both afterwards). The script prints `IP=…`/`PN=…`; eval captures them.
  deriveNetAddr = ''
    eval "$(PRIORITY_NETWORKS=${lib.escapeShellArg cfg.priorityNetworks} \
      ${scripts.derive-netaddr}/bin/doris-derive-netaddr.sh)"
  '';

  # Rewrite priority_networks in the conf at $CONF using the $PN set by deriveNetAddr.
  setPriorityNetworks = ''
    ${scripts.set-priority-networks}/bin/doris-set-priority-networks.sh "$CONF" "$PN"
  '';

  # Hardening shared by the FE and BE services. Both run the same lighter-than-
  # satellite sandbox: Doris mmaps data files, forks helpers, binds TCP ports and
  # loads JVM/JNI native code, so the strictest knobs (a seccomp SystemCallFilter
  # allowlist, ProtectSystem=strict) would break them. These settings drop all
  # capabilities, block privilege escalation, and make the kernel/hardware surface
  # read-only while leaving /var writable for state. LimitNOFILE is here too: BE's
  # preflight hard-fails under 60000 fds and FE needs the same headroom under load
  # (NixOS systemd default is 1024 soft).
  dorisHardening = {
    LimitNOFILE             = 655360;

    NoNewPrivileges         = true;
    ProtectSystem           = "full";
    ProtectHome             = true;
    PrivateTmp              = true;
    PrivateDevices          = true;
    ProtectKernelTunables   = true;
    ProtectKernelModules    = true;
    ProtectKernelLogs       = true;
    ProtectControlGroups    = true;
    RestrictRealtime        = true;
    RestrictSUIDSGID        = true;
    LockPersonality         = true;
    CapabilityBoundingSet   = "";
    AmbientCapabilities     = "";
    # AF_NETLINK is REQUIRED: both FE and BE call getifaddrs() at startup to
    # enumerate interfaces (BE's BackendOptions::init), which opens a NETLINK_ROUTE
    # socket. Without it here, socket(AF_NETLINK,…) is denied by this seccomp filter
    # and getifaddrs fails with EAFNOSUPPORT → "getifaddrs failed because Address
    # family not supported by protocol" → BE aborts at backend_options.cpp:49.
    RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" "AF_NETLINK" ];
  };
in
{
  options.services.doris = {
    enable = lib.mkEnableOption "Apache Doris FE and BE";

    package = lib.mkOption {
      type    = lib.types.package;
      default = dorisDefault;
      defaultText = lib.literalMD "Apache Doris 4.1.5 prebuilt binary (nix/doris.nix)";
      description = "The Doris derivation supplying fe/ and be/ subdirectories.";
    };

    jdk = lib.mkOption {
      type    = lib.types.package;
      default = pkgs.jdk17;
      defaultText = lib.literalMD "pkgs.jdk17";
      description = "JDK for FE (JVM) and BE (libjvm.so via JNI). Passed as JAVA_HOME.";
    };

    user = lib.mkOption {
      type    = lib.types.str;
      default = "aos-satellite";
      description = "System user that both FE and BE run as. Must already exist.";
    };

    group = lib.mkOption {
      type    = lib.types.str;
      default = "aos-satellite";
      description = "Primary group for the Doris service user.";
    };

    # Memory sizing. Upstream fe.conf/be.conf ship server-class defaults (FE: 8 GiB
    # JVM heap committed up front via -Xms; BE: mem_limit ≈ 90% of host RAM). Those
    # overcommit a modest appliance — the FE's 8 GiB -Xms fails with "insufficient
    # memory … commit_memory" on anything smaller. These two knobs size FE+BE to fit
    # an ~8 GiB host by default; raise them on a bigger box.
    feHeap = lib.mkOption {
      type    = lib.types.str;
      default = "2048m";
      description = "FE JVM heap (-Xmx and -Xms), e.g. \"2048m\" or \"4g\".";
    };

    beMemLimit = lib.mkOption {
      type    = lib.types.str;
      default = "4G";
      description = ''
        BE native memory ceiling (be.conf `mem_limit`). Accepts an absolute size
        (e.g. "4G") or a percentage of host RAM (e.g. "80%").
      '';
    };

    priorityNetworks = lib.mkOption {
      type    = lib.types.str;
      default = "auto";
      description = ''
        FE/BE `priority_networks` — the CIDR selecting which local IP each node
        advertises. MUST resolve to a real routable address, NOT loopback: the FE
        otherwise registers itself as 127.0.0.1 and advertises that as the master
        RPC address, and the BE's internal DNSCache (be/src/util/dns_cache.cpp)
        fails to resolve the "127.0.0.1" literal ("no cached ip available") — so the
        BE→FE disk/tablet report never connects and FE sees the backend with zero
        capacity ("Failed to find enough backend").

        "auto" (default) derives the CIDR at boot from the host's default-route
        interface (`ip route get`), so the image is portable across machines with
        no hardcoded IP. Set an explicit CIDR (e.g. "192.168.1.0/24") to pin it on
        an appliance with fixed networking.

        NOTE: the FE writes its self-identity into doris-meta at FIRST init and it
        is sticky — this only takes effect on a fresh metadata dir (a clean boot).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # ── Kernel tunables required by Doris BE's startup preflight ────────────────
    # BE memory-maps a very large number of segment/index files and refuses to
    # start unless vm.max_map_count ≥ 2000000. nixpkgs' sysctl.nix already sets
    # this to 1048576 at normal priority, which is below Doris's floor, so we must
    # override it with mkForce (a plain assignment would conflict; mkDefault would
    # lose). Set system-wide at boot: the BE service cannot write /proc/sys itself
    # because of its ProtectKernelTunables sandbox.
    boot.kernel.sysctl."vm.max_map_count" = lib.mkForce 2000000;

    # ── Frontend ───────────────────────────────────────────────────────────────
    systemd.services.doris-fe = {
      description = "Apache Doris Frontend";
      wantedBy    = [ "multi-user.target" ];
      # network-online.target: preStart derives priority_networks from the live
      # interface (deriveNetAddr), so the DHCP address/route must exist first.
      after       = [ "network-online.target" ];
      wants       = [ "network-online.target" ];

      environment = {
        # cfg.jdk.home (…/lib/openjdk), NOT the package root. The nixpkgs JDK puts
        # bin/java and lib/server/libjvm.so under lib/openjdk; start_*.sh derive
        # ${JAVA_HOME}/bin/java and ${JAVA_HOME}/lib/server for LD_LIBRARY_PATH, so
        # JAVA_HOME must be the real JDK home or libjvm.so won't be found.
        JAVA_HOME    = cfg.jdk.home;
        DORIS_HOME   = "/var/lib/doris/fe";
      };

      path = dorisScriptPath;

      preStart = ''
        STATE=/var/lib/doris/fe
        ${seedDorisHome "fe" "doris-meta"}
        # Real, writable metadata dir. The package ships an (empty) fe/doris-meta.
        # Only clear a stale *symlink* from the old seed logic — never a real
        # directory, which holds FE's metadata (and process.lock) across restarts.
        # `rm -f` on a directory fails ("Is a directory") and aborts preStart.
        [ -L "$STATE"/doris-meta ] && rm -f "$STATE"/doris-meta || true
        mkdir -p "$STATE"/doris-meta
        # Lay down a minimal fe.conf pointing data dirs at the state path.
        # Only written when absent so operator edits are preserved across upgrades.
        if [ ! -f "$STATE"/conf/fe.conf ]; then
          cp ${cfg.package}/fe/conf/fe.conf "$STATE"/conf/fe.conf
          chmod u+w "$STATE"/conf/fe.conf
          sed -i \
            -e 's|^#\?meta_dir.*|meta_dir = /var/lib/doris/fe/doris-meta|' \
            -e 's|^#\?sys_log_dir.*|sys_log_dir = /var/lib/doris/fe/log|' \
            -e 's|-Xmx[0-9]\+[kmgKMG]|-Xmx${cfg.feHeap}|g' \
            -e 's|-Xms[0-9]\+[kmgKMG]|-Xms${cfg.feHeap}|g' \
            "$STATE"/conf/fe.conf
          # Append keys that fe.conf does NOT ship (a sed substitution would no-op):
          # single-replica default for a one-BE cluster. priority_networks IS shipped
          # (commented), so setPriorityNetworks rewrites it in place.
          grep -qE '^#?[[:space:]]*default_replication_num[[:space:]]*=' "$STATE"/conf/fe.conf \
            || printf 'default_replication_num = 1\n' >> "$STATE"/conf/fe.conf
          CONF="$STATE"/conf/fe.conf
          ${deriveNetAddr}
          ${setPriorityNetworks}
        fi
      '';

      serviceConfig = {
        Type            = "forking";
        User            = cfg.user;
        Group           = cfg.group;
        # start_fe.sh writes its pid to ${PID_DIR}/fe.pid, and PID_DIR is computed
        # UNCONDITIONALLY as the script's own bin/ directory (`cd "$(dirname "$0")";
        # pwd`), ignoring any env override. So the pid lands in bin/, not the home
        # root — PIDFile must match or systemd reports "Can't open PID file … after
        # start" and marks the unit failed.
        PIDFile         = "/var/lib/doris/fe/bin/fe.pid";
        WorkingDirectory = "/var/lib/doris/fe";

        # systemd creates /var/lib/doris + /var/lib/doris/fe owned by the service
        # user with mode 0750 BEFORE preStart runs, so preStart never has to chmod
        # a dir it doesn't own (the source of the earlier "cannot change
        # permissions" failure). State persists across restarts.
        StateDirectory     = "doris/fe";
        StateDirectoryMode = "0750";

        ExecStart  = "/var/lib/doris/fe/bin/start_fe.sh --daemon";
        ExecStop   = "/var/lib/doris/fe/bin/stop_fe.sh";
        Restart    = "on-failure";
        RestartSec = 10;
      } // dorisHardening;
    };

    # ── Backend ────────────────────────────────────────────────────────────────
    systemd.services.doris-be = {
      description = "Apache Doris Backend";
      wantedBy    = [ "multi-user.target" ];
      # BE can start in parallel with FE; FE registers BE via SQL after both are up.
      # network-online.target: preStart derives priority_networks from the live
      # interface, same as FE.
      after       = [ "network-online.target" ];
      wants       = [ "network-online.target" ];

      environment = {
        # BE runs a JVM for JNI/Java UDFs and hard-fails at startup if JAVA_HOME is
        # unset ("The JAVA_HOME environment variable is not set"). Same JDK as FE.
        # Must be cfg.jdk.home (…/lib/openjdk) so start_be.sh's LD_LIBRARY_PATH
        # (${JAVA_HOME}/lib/server) actually contains libjvm.so — doris_be NEEDs it
        # and we intentionally left it out of the ELF's RPATH (resolved at runtime).
        JAVA_HOME    = cfg.jdk.home;
        DORIS_HOME   = "/var/lib/doris/be";
        # doris_be is autopatchelf'd to run under nixpkgs glibc (see nix/doris.nix).
        # Put that glibc's lib dir on the loader path so the binary can dlopen its
        # matching NSS modules (libnss_files.so.2, …) for name resolution. start_be.sh
        # preserves an inherited LD_LIBRARY_PATH (it appends the JVM dirs). NOTE: this
        # is necessary but NOT sufficient for FE/BE comms — the "can't resolve
        # 127.0.0.1" report failure is fixed by priority_networks (see
        # setPriorityNetworks), which stops the loopback literal being used at all.
        LD_LIBRARY_PATH = "${pkgs.glibc}/lib";
      };

      path = dorisScriptPath;

      preStart = ''
        STATE=/var/lib/doris/be
        ${seedDorisHome "be" "storage"}
        # Real, writable storage dir (package ships an empty be/storage). Only a
        # stale *symlink* from the old seed logic should be cleared — never a real
        # directory, which holds BE's data across restarts. `rm -f` on a directory
        # fails ("Is a directory") and aborts preStart; test for a symlink instead.
        [ -L "$STATE"/storage ] && rm -f "$STATE"/storage || true
        mkdir -p "$STATE"/storage
        if [ ! -f "$STATE"/conf/be.conf ]; then
          cp ${cfg.package}/be/conf/be.conf "$STATE"/conf/be.conf
          chmod u+w "$STATE"/conf/be.conf
          sed -i \
            -e 's|^#\?storage_root_path.*|storage_root_path = /var/lib/doris/be/storage|' \
            -e 's|^#\?sys_log_dir.*|sys_log_dir = /var/lib/doris/be/log|' \
            "$STATE"/conf/be.conf
          # be.conf ships WITHOUT a mem_limit key, so BE defaults to ~90% of host
          # RAM — too much for a modest appliance. Set an explicit ceiling: rewrite
          # if a key exists, otherwise append.
          if grep -qE '^#?[[:space:]]*mem_limit' "$STATE"/conf/be.conf; then
            sed -i 's|^#\?[[:space:]]*mem_limit.*|mem_limit = ${cfg.beMemLimit}|' "$STATE"/conf/be.conf
          else
            printf 'mem_limit = %s\n' '${cfg.beMemLimit}' >> "$STATE"/conf/be.conf
          fi
          # Advertise a routable IP, not loopback (see setPriorityNetworks).
          CONF="$STATE"/conf/be.conf
          ${deriveNetAddr}
          ${setPriorityNetworks}
        fi
      '';

      serviceConfig = {
        # start_be.sh's --daemon branch backgrounds doris_be with `&` but — unlike
        # start_fe.sh — NEVER writes a pid file (the `pidfile` var is only read to
        # detect an already-running instance). So Type=forking + PIDFile can never
        # be satisfied: systemd waits for a pid file that never appears, times out
        # ("Can't open PID file … after start"), and kills the unit.
        #
        # Run BE in the foreground with --console (RUN_CONSOLE) instead and let
        # systemd supervise doris_be directly as Type=simple — no pid file, correct
        # SIGTERM-based stop, proper restart semantics. --console also sets
        # DORIS_LOG_TO_STDERR=1, so BE logs land in the journal.
        Type            = "simple";
        User            = cfg.user;
        Group           = cfg.group;
        WorkingDirectory = "/var/lib/doris/be";

        # systemd creates /var/lib/doris + /var/lib/doris/be owned by the service
        # user with mode 0750 before preStart runs (see the FE note above).
        StateDirectory     = "doris/be";
        StateDirectoryMode = "0750";

        ExecStart  = "/var/lib/doris/be/bin/start_be.sh --console";
        Restart    = "on-failure";
        RestartSec = 10;
      } // dorisHardening;
    };

    # ── One-time cluster bootstrap: register BE with FE ─────────────────────────
    # A BE does not join on its own — the FE must be told about it via
    # `ALTER SYSTEM ADD BACKEND`, after which FE starts heartbeating it. Without
    # this, BE sits forever at "waiting to receive first heartbeat from frontend"
    # and FE has zero live backends, so every storage DDL fails
    # ("System has no available disk"). This unit forms the single-node cluster at
    # boot: derive the node's routable IP (same as the confs — never loopback, which
    # the BE's DNSCache can't resolve), wait for FE's query port, then register
    # <IP>:9050 (the BE heartbeat_service_port) if not already present. Idempotent.
    systemd.services.doris-register-be = {
      description = "Register the Doris BE with the FE (one-time cluster bootstrap)";
      wantedBy = [ "multi-user.target" ];
      # network-online.target: the auto IP derivation needs the interface to have
      # its (DHCP) address and default route. Without this the unit can fire before
      # the lease lands, `ip route get` returns empty, and it exits 1 ("could not
      # derive backend IP"). Order + pull in network-online so the route exists.
      after    = [ "doris-fe.service" "doris-be.service" "network-online.target" ];
      wants    = [ "doris-fe.service" "doris-be.service" "network-online.target" ];

      path = [ pkgs.coreutils pkgs.gnugrep pkgs.iproute2 pkgs.gawk ];

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        User  = cfg.user;
        Group = cfg.group;
      };

      script = ''
        # All the logic — derive routable IP (never loopback), wait for FE via
        # `SHOW BACKENDS`, idempotent `ALTER SYSTEM ADD BACKEND <IP>:9050` — lives in the
        # shared wrapped script. mariadb.client's `mysql` is baked onto its PATH.
        PRIORITY_NETWORKS=${lib.escapeShellArg cfg.priorityNetworks} \
        MYSQL_BIN=mysql \
        FE_HOST=127.0.0.1 \
        FE_PORT=9030 \
          ${scripts.register-be}/bin/doris-register-be.sh
      '';
    };
  };
}
