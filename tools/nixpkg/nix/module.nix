# module.nix — NixOS module for the AOS satellite
#
# A hardened systemd service running the verified satellite payload under a dedicated
# unprivileged system user. Hardening aligns with ADR-024's kernel primitives
# (namespaces, cgroups v2, seccomp): NoNewPrivileges, ProtectSystem=strict,
# ProtectHome, PrivateTmp, SystemCallFilter/seccomp, empty CapabilityBoundingSet.
#
# FIPS: this module does NOT set `fips=1`; that is a whole-host concern set on the
# nixosConfigurations.satellite-host system config (flake.nix) so the kernel boots in
# FIPS mode, not only the service.

{ config, lib, pkgs, ... }:

let
  cfg = config.services.aosSatellite;

  # Rebuild nixpkgs with the pinned-OCM overlay against THIS host's system, so the
  # module can supply a verified satellite package by default — importing it into an
  # existing NixOS configuration needs only `services.aosSatellite.enable = true;`.
  # (The flake's own outputs use the same overlay; this keeps the module usable
  # standalone, without the consumer having to replicate that wiring.)
  pkgsOcm = import pkgs.path {
    inherit (pkgs) system;
    overlays = [ (final: prev: { ocm = import ./ocm.nix { pkgs = prev; }; }) ];
  };
  verifiedPackage = import ./satellite-package.nix {
    pkgs = pkgsOcm;
    ctfVerified = import ./satellite-ctf.nix { pkgs = pkgsOcm; };
  };
in
{
  options.services.aosSatellite = {
    enable = lib.mkEnableOption "the AOS satellite service";

    package = lib.mkOption {
      type = lib.types.package;
      default = verifiedPackage;
      defaultText = lib.literalMD "the signature-verified satellite built for this host's system";
      description = ''
        The signature-verified satellite package (see nix/satellite-package.nix).
        Its mainProgram (lib.getExe) is run as the service executable.

        Defaults to the package built for the consuming host's own `pkgs.system`,
        so importing this module into an existing NixOS configuration needs only
        `services.aosSatellite.enable = true;` — no manual package wiring. Override
        to pin a prebuilt closure (e.g. from a local substituter, air-gapped).
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "aos-satellite";
      description = "Unprivileged system user the satellite runs as.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "aos-satellite";
      description = "Primary group of the satellite service user.";
    };

    extraArgs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Extra command-line arguments passed to the satellite binary.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
      description = "AOS satellite service user";
      # Pin a stable UID/GID. Doris (and the satellite) keep persistent state on the
      # image's ext4 partition (doris-meta, storage, logs) that SURVIVES rebuilds.
      # With a dynamically-allocated system UID, a rebuild could hand aos-satellite a
      # different number, leaving that persisted state owned by a now-dead UID — the
      # service then can't chmod/write its own data dir ("cannot change permissions
      # of .../doris-meta"). A fixed id keeps on-disk ownership valid across every
      # generation. 496 is in NixOS's local static system range (400–499) and is not
      # one of the documented reserved ids.
      uid = 496;
    };
    users.groups.${cfg.group} = { gid = 496; };

    systemd.services.aos-satellite = {
      description = "AOS satellite";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];

      serviceConfig = {
        ExecStart = lib.escapeShellArgs (
          [ (lib.getExe cfg.package) ] ++ cfg.extraArgs
        );
        Restart = "on-failure";
        RestartSec = 5;

        User = cfg.user;
        Group = cfg.group;

        # ── Hardening (ADR-024 kernel primitives) ───────────────────────────
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;        # cgroups v2 isolation
        ProtectClock = true;
        ProtectHostname = true;
        ProtectProc = "invisible";
        ProcSubset = "pid";
        RestrictNamespaces = true;          # namespaces
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        RemoveIPC = true;

        # seccomp system-call filter: allow only the well-known-safe set.
        SystemCallFilter = [ "@system-service" "~@privileged" "~@resources" ];
        SystemCallArchitectures = "native";

        # Drop all capabilities; the satellite needs none.
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";

        # Restrict address families to the usual local + IP sockets.
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];

        # Writable runtime/state dirs under systemd's managed hierarchy.
        RuntimeDirectory = "aos-satellite";
        StateDirectory = "aos-satellite";
        WorkingDirectory = "%S/aos-satellite";
        UMask = "0077";
      };
    };
  };
}
