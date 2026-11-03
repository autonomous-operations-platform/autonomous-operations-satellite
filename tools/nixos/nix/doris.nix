# doris.nix — Apache Doris 4.1.5 packaged from the upstream prebuilt release.
#
# Apache Doris is not in nixpkgs. This derivation fetches the upstream prebuilt
# release tarball (pinned by hash), and patches its binaries to run on NixOS. The
# tarball ships two components:
#
#   fe/   — Frontend (Java, query planning, HTTP/MySQL interface)
#   be/   — Backend  (C++, storage engine, query execution)
#
# Both are installed under $out/{fe,be} and can be started with the upstream
# shell scripts at $out/fe/bin/start_fe.sh and $out/be/bin/start_be.sh.
# The NixOS module (doris-module.nix) wires them into systemd services and
# supplies JAVA_HOME from the configured JDK package.
#
# Arch notes: Doris ships separate x64 and arm64 tarballs. The x64 tarball also
# has an AVX2 variant; the non-AVX2 build is used here for broader compatibility.

{ pkgs }:

let
  inherit (pkgs) stdenv lib;

  version = "4.1.5";

  asset = {
    "x86_64-linux" = {
      filename = "apache-doris-${version}-bin-x64.tar.gz";
      hash = "sha512-7HZgpM7wXskTSRSUG7bC898NXBkxmzFGn9wbDtt9ppfyJWaaPzH3SReLFd9e8r/KDNmFcmwcApu6sTPrXLNxtg==";
    };
    "aarch64-linux" = {
      filename = "apache-doris-${version}-bin-arm64.tar.gz";
      hash = "sha512-AKwYGV/iMmgnZ1fP0W46SQ5tx0cDSMVTpbr4+TOqDnzYTHoB++j7PVghaGz/ByQq70BDbFU+vTtFmco7SbNdPw==";
    };
  }.${stdenv.hostPlatform.system}
    or (throw "doris.nix: unsupported system ${stdenv.hostPlatform.system}");

  src = pkgs.fetchurl {
    url = "https://apache-doris-releases.oss-accelerate.aliyuncs.com/${asset.filename}";
    inherit (asset) hash;
  };
in
pkgs.stdenv.mkDerivation {
  pname = "apache-doris";
  inherit version;

  inherit src;

  # The BE storage engine (be/lib/doris_be) is a prebuilt generic-Linux ELF: its
  # interpreter is /lib/ld-linux-aarch64.so.1 and it NEEDs glibc sonames — none of
  # which exist on NixOS, so it fails with "NixOS cannot run dynamically linked
  # executables intended for generic linux environments". autoPatchelfHook rewrites
  # every shipped ELF's interpreter + RPATH to Nix-store equivalents at build time,
  # producing a store-only closure with no runtime FHS shim.
  nativeBuildInputs = [ pkgs.autoPatchelfHook ];

  # doris_be's only dynamic deps are glibc (libc/libm/libdl/libpthread/librt/
  # libresolv) plus libjvm.so. glibc is provided here; libstdc++/libgcc from the
  # C++ toolchain cover any C++ ELF that needs them (the binaries otherwise ship
  # pre-linked C++ libs statically).
  buildInputs = [
    (lib.getLib stdenv.cc.cc)   # libstdc++ / libgcc_s
    pkgs.glibc
  ];

  # libjvm.so is NOT a standard library package — it lives at $JAVA_HOME/lib/server
  # and is resolved at RUNTIME via LD_LIBRARY_PATH (start_be.sh exports it from
  # JAVA_HOME). Tell autoPatchelf not to fail the build over it.
  autoPatchelfIgnoreMissingDeps = [ "libjvm.so" ];

  installPhase = ''
    runHook preInstall

    # stdenv already cd-s into the unpacked source root (apache-doris-*-bin-*),
    # so fe/ and be/ are directly available here — no top-level search needed.
    mkdir -p "$out"
    cp -a fe "$out/fe"
    cp -a be "$out/be"

    # Drop arthas/ (a JVM attach/diagnostic agent, loaded on demand — never needed
    # to run FE). It ships x86_64 .so files that would fail aarch64 autopatchelf,
    # and it's unwanted attack surface in a hardened image.
    rm -rf "$out/fe/arthas"

    # Expose start/stop scripts as top-level bin/ entries for convenience.
    mkdir -p "$out/bin"
    ln -s "$out/fe/bin/start_fe.sh" "$out/bin/start-fe"
    ln -s "$out/be/bin/start_be.sh" "$out/bin/start-be"
    ln -s "$out/fe/bin/stop_fe.sh"  "$out/bin/stop-fe"
    ln -s "$out/be/bin/stop_be.sh"  "$out/bin/stop-be"

    runHook postInstall
  '';

  meta = with lib; {
    description = "Apache Doris — real-time analytical database (FE + BE)";
    homepage    = "https://doris.apache.org";
    license     = licenses.asl20;
    platforms   = [ "x86_64-linux" "aarch64-linux" ];
    sourceProvenance = [ sourceTypes.binaryNativeCode ];
  };
}
