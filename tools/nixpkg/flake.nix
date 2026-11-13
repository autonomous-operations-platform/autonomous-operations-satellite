{
  description = "AOS satellite host — NixOS image packaging the signature-verified OCM satellite artifact (ADR-024: combined delivery, air-gapped, FIPS)";

  inputs = {
    # Pinned release branch; the committed flake.lock pins the exact revision by hash.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";

    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixos-generators, ... }:
    let
      # arm64 is the native Colima VM arch (fast); amd64 is reachable via binfmt/QEMU.
      systems = [ "aarch64-linux" "x86_64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);

      # Overlay the pinned OCM 0.15.0 (new rewrite, has `verify component-version`)
      # over nixpkgs' legacy `ocm`, so every build-time verify/extract uses it.
      ocmOverlay = final: prev: {
        ocm = import ./nix/ocm.nix { pkgs = prev; };
      };

      # Overlay Apache Doris (not in nixpkgs). Default JDK is jdk17; callers can
      # override via pkgs.doris.override { jdk = pkgs.jdk21; } if needed.
      dorisOverlay = final: prev: {
        doris = import ./nix/doris.nix { pkgs = prev; };
      };

      pkgsFor = system: import nixpkgs {
        inherit system;
        overlays = [ ocmOverlay dorisOverlay ];
      };

      # ── Build the verified-artifact chain for a given system ───────────────
      chainFor = system:
        let
          pkgs = pkgsFor system;
          ctfVerified = import ./nix/satellite-ctf.nix { inherit pkgs; };
          satellite = import ./nix/satellite-package.nix { inherit pkgs ctfVerified; };
        in
        { inherit pkgs ctfVerified satellite; };

      # ── System modules shared by nixosConfigurations and the image ─────────
      # Single source of truth: the toplevel switch closure and every image format
      # are built from exactly these modules.
      systemModulesFor = system: satellite: [
        self.nixosModules.default
        self.nixosModules.doris
        ./nix/fips.nix
        ({ ... }: {
          nixpkgs.hostPlatform = system;

          services.aosSatellite = {
            enable = true;
            package = satellite;
            fips.rebuildKernel = false;
          };

          services.doris.enable = true;

          # FIPS 140-2/3 (ADR-024) is wired by ./nix/fips.nix above. It sets fips=1 by
          # default, but the CONFIG_CRYPTO_FIPS kernel REBUILD is opt-in
          # (services.aosSatellite.fips.rebuildKernel, default OFF) because it forces a
          # full kernel compile. As shipped, this host sets fips=1 on the stock kernel —
          # where fips=1 is INERT — so FIPS is NOT active at runtime here. For the real
          # appliance build, set fips.rebuildKernel = true and confirm with the VM boot
          # test (checks.<system>.boot-test), which enables it and asserts
          # /proc/sys/crypto/fips_enabled == 1.

          # Air-gapped, generation-based A/B switch (ADR-024): the toplevel closure
          # is copied to the target and activated offline with `nixos-rebuild switch`.
          system.stateVersion = "25.05";
        })
      ];

      # Minimal bootable baseline for the bare `nixosConfigurations.satellite-host`
      # toplevel closure (A/B switch target). The nixos-generators image formats
      # supply their OWN filesystem/bootloader wiring, so this is kept OUT of the
      # shared systemModules and applied only to nixosConfigurations below. On real
      # hardware it is replaced by the host's generated hardware-configuration.nix.
      bootBaselineModule = { ... }: {
        boot.loader.systemd-boot.enable = true;
        boot.loader.efi.canTouchEfiVariables = false;
        fileSystems."/" = {
          device = "/dev/disk/by-label/nixos";
          fsType = "ext4";
        };
        fileSystems."/boot" = {
          device = "/dev/disk/by-label/ESP";
          fsType = "vfat";
        };
      };
    in
    {
      # ── Packages ───────────────────────────────────────────────────────────
      packages = forAllSystems (system:
        let
          c = chainFor system;
          systemModules = systemModulesFor system c.satellite;
        in
        {
          # The signature-verified satellite payload (step 2).
          satellite = c.satellite;
          default = c.satellite;

          # Apache Doris FE + BE (not in nixpkgs); pinned prebuilt binary.
          doris = c.pkgs.doris;

          # The verify derivation (step 1) as a first-class, independently buildable
          # gate — this is what `mise run verify-ctf` builds.
          ctf-verified = c.ctfVerified;

          # Bootable qcow2 image, built host-side via systemd-repart (see nix/image.nix) —
          # NO nested QEMU/TCG VM, so the build is bounded by copying the closure
          # (minutes) rather than emulating a CPU (~40 min). qcow2 boots directly under
          # QEMU/Colima for the FIPS verification step.
          image = import ./nix/image.nix {
            inherit nixpkgs system systemModules;
          };
          image-qcow = self.packages.${system}.image;

          # Debug image: the same hardened system PLUS ./nix/debug.nix (serial console
          # + root password). This is what `mise run run` boots so you get a shell.
          # Break-glass access is confined here and never reaches `image`.
          image-debug = import ./nix/image.nix {
            inherit nixpkgs system;
            systemModules = systemModules ++ [ ./nix/debug.nix ];
          };
        });

      # ── NixOS modules ────────────────────────────────────────────────────────
      nixosModules.default = import ./nix/module.nix;
      nixosModules.doris   = import ./nix/doris-module.nix;

      # ── Apps: `nix run github:org/aos-host-package` runs the satellite ─────────
      apps = forAllSystems (system:
        let c = chainFor system; in
        {
          default = {
            type = "app";
            program = "${c.satellite}/bin/f5-vlan-demo-satellite";
          };
          satellite = self.apps.${system}.default;
        });

      # ── NixOS system (the air-gapped `nixos-rebuild switch` toplevel closure) ──
      nixosConfigurations.satellite-host =
        let
          system = "aarch64-linux";
          c = chainFor system;
        in
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = systemModulesFor system c.satellite ++ [ bootBaselineModule ];
        };

      # Debug variant of the toplevel closure: hardened system + break-glass console
      # access (./nix/debug.nix). Used for local boot/iteration; never published.
      nixosConfigurations.satellite-host-debug =
        let
          system = "aarch64-linux";
          c = chainFor system;
        in
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = systemModulesFor system c.satellite
            ++ [ bootBaselineModule ./nix/debug.nix ];
        };

      # ── Checks: `nix flake check` / `nix build .#checks.<system>.boot-test` ────
      # A NixOS VM test that boots the satellite host under QEMU and asserts the
      # ADR-024 runtime invariants: FIPS active + hardened service running.
      checks = forAllSystems (system:
        let c = chainFor system; in
        {
          boot-test = import ./nix/boot-test.nix {
            inherit (c) pkgs satellite;
          };
        });

      # ── Dev shell ──────────────────────────────────────────────────────────
      devShells = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.ocm pkgs.doris nixos-generators.packages.${system}.default ];
          };
        });
    };
}
