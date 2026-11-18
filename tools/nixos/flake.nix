{
  description = "AOS satellite host — the NixOS appliance image. Consumes the tools/nixpkg package flake (satellite payload, Doris, modules) and assembles a bootable, hardened, FIPS-wired NixOS system (ADR-024).";

  inputs = {
    # The package layer: signature-verified satellite, Doris, and the reusable NixOS
    # modules (nixosModules.default = the satellite service, nixosModules.doris).
    #
    # LOCAL DEVELOPMENT (current): a relative path to the sibling package flake. Note two
    # consequences of a relative `path:` input:
    #   - It only resolves when THIS flake is accessed via the git tree, e.g.
    #     `nix build 'git+file:///path/to/repo?dir=tools/nixos#image'`. A `path:` ref to
    #     this directory resolves `../nixpkg` against the store copy and fails.
    #   - It is NOT locked to a revision (the lock just records `path:../nixpkg`), so the
    #     build always tracks whatever is on disk next door.
    #
    # ONCE tools/nixpkg IS PUBLISHED (e.g. its own GitHub repo), replace the line below
    # with the published ref so the image pins an exact, fetchable revision:
    #
    #     aos-nixpkg.url = "github:autonomous-operations-platform/aos-nixpkg";
    #     # or pin a tag/commit:
    #     # aos-nixpkg.url = "github:autonomous-operations-platform/aos-nixpkg/v0.0.3";
    #
    # then run `nix flake update aos-nixpkg` to write the locked revision into flake.lock.
    # Nothing else in this file changes — the outputs consume `aos-nixpkg` the same way
    # regardless of where it is fetched from. If the published repo puts the flake in a
    # subdirectory, add `?dir=tools/nixpkg` to the URL.
    aos-nixpkg.url = "path:../nixpkg";

    # Follow the package layer's nixpkgs so the whole system is built from one pin.
    nixpkgs.follows = "aos-nixpkg/nixpkgs";
  };

  outputs = { self, nixpkgs, aos-nixpkg, ... }:
    let
      # arm64 is the native Colima VM arch (fast); amd64 is reachable via binfmt/QEMU.
      systems = [ "aarch64-linux" "x86_64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);

      # The verified satellite package, per system, from the package flake.
      satelliteFor = system: aos-nixpkg.packages.${system}.satellite;

      # nixpkgs with the package flake's overlays (ocm + doris), so the Doris module's
      # `pkgs.doris` default and any ocm use resolve to the pinned versions. We reuse the
      # same overlay definitions by importing the package flake's doris.nix/ocm.nix is not
      # needed here — the modules carry their own package defaults — but the Doris module
      # wants `pkgs.doris`, so apply the doris overlay for the system config's pkgs.
      pkgsFor = system: import nixpkgs {
        inherit system;
        overlays = [
          (final: prev: { doris = prev.callPackage (aos-nixpkg + "/nix/doris.nix") { pkgs = prev; }; })
        ];
      };

      # ── System modules shared by nixosConfigurations and the image ─────────
      # Single source of truth: the toplevel switch closure and every image format are
      # built from exactly these modules. The satellite + Doris modules come from the
      # package flake; fips.nix + the config glue live here in the image layer.
      systemModulesFor = system: satellite: [
        aos-nixpkg.nixosModules.default
        aos-nixpkg.nixosModules.doris
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
          # (services.aosSatellite.fips.rebuildKernel, default OFF). As shipped, this host
          # sets fips=1 on the stock kernel — where fips=1 is INERT — so FIPS is NOT active
          # at runtime here. For the real appliance build, set fips.rebuildKernel = true
          # and confirm with checks.<system>.boot-test.
          system.stateVersion = "25.05";
        })
      ];

      # Minimal bootable baseline for the bare nixosConfigurations toplevel closure (the
      # A/B switch target). The image formats supply their OWN fs/bootloader wiring, so
      # this is applied only to nixosConfigurations below. On real hardware it is replaced
      # by the host's generated hardware-configuration.nix.
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
      # ── Packages: the bootable images ───────────────────────────────────────
      packages = forAllSystems (system:
        let
          satellite = satelliteFor system;
          systemModules = systemModulesFor system satellite;
        in
        {
          # Bootable qcow2 image, built host-side via systemd-repart (nix/image.nix) —
          # no nested QEMU/TCG VM.
          image = import ./nix/image.nix { inherit nixpkgs system systemModules; };
          default = self.packages.${system}.image;
          image-qcow = self.packages.${system}.image;

          # Debug image: same hardened system PLUS ./nix/debug.nix (serial console + root
          # password) for local boot/iteration. Never published.
          image-debug = import ./nix/image.nix {
            inherit nixpkgs system;
            systemModules = systemModules ++ [ ./nix/debug.nix ];
          };
        });

      # ── NixOS system (the air-gapped `nixos-rebuild switch` toplevel closure) ──
      nixosConfigurations.satellite-host =
        let
          system = "aarch64-linux";
          satellite = satelliteFor system;
        in
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = systemModulesFor system satellite ++ [ bootBaselineModule ];
        };

      nixosConfigurations.satellite-host-debug =
        let
          system = "aarch64-linux";
          satellite = satelliteFor system;
        in
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = systemModulesFor system satellite
            ++ [ bootBaselineModule ./nix/debug.nix ];
        };

      # ── Checks: `nix build .#checks.<system>.boot-test` ────────────────────────
      # NixOS VM test: boots the satellite host under QEMU and asserts the ADR-024
      # runtime invariants (FIPS active + hardened service running).
      checks = forAllSystems (system:
        let
          pkgs = pkgsFor system;
          satellite = satelliteFor system;
        in
        {
          boot-test = import ./nix/boot-test.nix {
            inherit pkgs satellite;
            satelliteModule = aos-nixpkg.nixosModules.default;
          };
        });
    };
}
