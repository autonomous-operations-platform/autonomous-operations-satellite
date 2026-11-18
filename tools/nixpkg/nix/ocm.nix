# ocm.nix — pinned OCM CLI (the build-time verifier), as a fixed-output derivation
#
# nixpkgs ships the LEGACY OCM CLI (open-component-model/ocm, v0.x), which has no
# `verify component-version` subcommand. The build-time signature gate needs the NEW
# Open Component Model rewrite (open-component-model/open-component-model). We pin its
# release binary v0.15.0 — matching the `ocm` the .mise.toml pins for Mac-side helpers
# and the version the verify/download commands were confirmed against.
#
# Pinning the verifier by exact version + sha256 is itself a supply-chain control:
# the tool that enforces provenance cannot silently drift.

{ pkgs }:

let
  inherit (pkgs) stdenv lib;

  version = "0.15.0";

  # Prebuilt static release binaries, per Nix build platform.
  asset = {
    "aarch64-linux" = {
      name = "ocm-linux-arm64";
      hash = "sha256-WfuSAGseRPMWTq81/hKg14sST3Lg2ML5q3XeZWZ9zsU=";
    };
    "x86_64-linux" = {
      name = "ocm-linux-amd64";
      hash = "sha256-vcnLO1AN1L0kgHitQnemvyHYCbGxrv8rJkItFQG8XJQ=";
    };
  }.${stdenv.hostPlatform.system}
    or (throw "ocm.nix: unsupported system ${stdenv.hostPlatform.system}");

  bin = pkgs.fetchurl {
    url = "https://github.com/open-component-model/open-component-model/releases/download/v${version}/${asset.name}";
    inherit (asset) hash;
  };
in
pkgs.runCommand "ocm-${version}"
  {
    meta = with lib; {
      description = "Open Component Model CLI (new rewrite) ${version}, pinned build-time verifier";
      platforms = [ "aarch64-linux" "x86_64-linux" ];
      mainProgram = "ocm";
    };
  }
  ''
    install -D -m0755 ${bin} "$out/bin/ocm"
  ''
