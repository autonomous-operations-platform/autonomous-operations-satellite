{
  description = "AOS satellite packaging — signature-verified OCM satellite payload, Apache Doris, and reusable NixOS modules + a portable systemd installer. The NixOS image layer (tools/nixos) consumes this flake.";

  inputs = {
    # Pinned release branch; the committed flake.lock pins the exact revision by hash.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
  };

  outputs = { self, nixpkgs, ... }:
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
    in
    {
      # ── Packages ───────────────────────────────────────────────────────────
      packages = forAllSystems (system:
        let c = chainFor system; in
        {
          # The signature-verified satellite payload.
          satellite = c.satellite;
          default = c.satellite;

          # Apache Doris FE + BE (not in nixpkgs); pinned prebuilt binary.
          doris = c.pkgs.doris;

          # The verify derivation as a first-class, independently buildable gate —
          # this is what `mise run verify-ctf` builds.
          ctf-verified = c.ctfVerified;

          # Portable systemd integration: unit files + aos-ctl.sh installer for running
          # the whole stack (AOS satellite + Doris FE/BE) on any systemd Linux with Nix,
          # not just NixOS. Shares the Doris runtime logic with the NixOS module via
          # nix/doris-scripts.nix; the satellite unit mirrors nix/module.nix's hardening.
          #   nix build .#satellite-systemd && sudo ./result/aos-ctl.sh install
          satellite-systemd = import ./nix/satellite-systemd.nix {
            pkgs = c.pkgs;
            inherit (c) satellite;
          };
        });

      # ── NixOS modules (consumed by the tools/nixos image layer) ────────────────
      nixosModules.default = import ./nix/module.nix;
      nixosModules.doris   = import ./nix/doris-module.nix;

      # ── Apps: `nix run .#satellite` runs the satellite ─────────────────────────
      apps = forAllSystems (system:
        let c = chainFor system; in
        {
          default = {
            type = "app";
            program = "${c.satellite}/bin/f5-vlan-demo-satellite";
          };
          satellite = self.apps.${system}.default;
        });

      # ── Dev shell: ocm + doris on PATH ─────────────────────────────────────────
      devShells = forAllSystems (system:
        let pkgs = pkgsFor system; in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.ocm pkgs.doris ];
          };
        });
    };
}
