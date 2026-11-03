# satellite-package.nix — extract runnable artifacts from the verified CTF
#
# Consumes the *verified* CTF output from satellite-ctf.nix (step 1) and pulls the
# architecture-matched payload binary out of it into a normal Nix package with the
# executable in $out/bin. Because this derivation takes `ctfVerified` as input, the
# signature gate is strictly upstream — unverified bytes can never reach here.
#
# The signed component carries one resource (the satellite payload), with per-arch
# blobs selected by extraIdentity {architecture, os}. The resource name matches the
# component's leaf name (see resourceName below). Extracted live with ocm 0.15.0:
#   ocm download resource <ctf>//<ref> \
#     --identity name=<satellite>,architecture=<arch> \
#     --extraction-policy disable --output <file>
# which yields the exact static ELF for the target arch.
#
# NOTE on a separate `satellite` *runtime*: an unsigned runtime component may live
# in a SEPARATE CTF (e.g. aos-demo, resource `satellite`) not part of the vendored
# signed tarball. When signed and published it can be added as a second verified
# source + extraction here, identically. For now this package ships the signed
# payload the supply-chain gate actually covers.

{ pkgs, ctfVerified }:

let
  inherit (pkgs) lib stdenv;

  # Map the Nix build platform to the OCM resource `architecture` extraIdentity.
  ocmArch = {
    "aarch64-linux" = "arm64";
    "x86_64-linux" = "amd64";
  }.${stdenv.hostPlatform.system}
    or (throw "satellite-package: unsupported system ${stdenv.hostPlatform.system}");

  componentRef = ctfVerified.componentRef;   # the signed component:version
  resourceName = "f5-vlan-demo-satellite";    # OCM resource = component leaf name
in
pkgs.stdenvNoCC.mkDerivation {
  pname = "f5-vlan-demo-satellite";
  version = "0.0.3";

  dontUnpack = true;

  nativeBuildInputs = [ pkgs.ocm ];

  buildPhase = ''
    runHook preBuild

    install -d "$out/bin"

    # Extract the arch-matched payload binary from the verified CTF. `ctfVerified`
    # is the verified CTF directory (a /nix/store path) produced by step 1.
    echo ">>> ocm download resource ${resourceName} (architecture=${ocmArch}) ..."
    ocm download resource \
      "${ctfVerified}//${componentRef}" \
      --identity name=${resourceName},architecture=${ocmArch} \
      --extraction-policy disable \
      --output "$out/bin/${resourceName}"

    chmod +x "$out/bin/${resourceName}"

    # Sanity: the payload must be a Linux ELF for the target arch.
    if ! head -c4 "$out/bin/${resourceName}" | grep -q $'\x7fELF'; then
      echo "ERROR: extracted resource is not an ELF binary" >&2
      exit 1
    fi

    runHook postBuild
  '';

  dontInstall = true;

  # The upstream verified CTF, exposed for inspection / reuse.
  passthru = { inherit ctfVerified; };

  meta = with lib; {
    description = "Satellite payload binary, extracted from the signature-verified OCM CTF";
    platforms = [ "aarch64-linux" "x86_64-linux" ];
    mainProgram = resourceName;
  };
}
