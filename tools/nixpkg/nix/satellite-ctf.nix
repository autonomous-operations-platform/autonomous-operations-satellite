# satellite-ctf.nix — source toggle + build-time OCM signature verification
#
# This is the supply-chain heart of the design. Two stages:
#
#   (a) Source: a one-line toggle choosing where the signed CTF bytes come from.
#       Vendored tarball today (offline, pre-publish); `fetchurl` from GitHub later.
#       Both are fixed-output and pinned by the SAME sha256, so everything downstream
#       is byte-identical and the switch is invisible to the rest of the build.
#
#   (b) Verified-unpack derivation: unpack the CTF, then run `ocm verify
#       component-version` against the RSASSA-PSS public key. `ocm` exits non-zero on
#       a bad/absent signature, which FAILS the Nix build. The derivation output is
#       the verified CTF directory, so Nix content-addressing ties the output hash to
#       *verified-signed* bytes — nothing unverified can flow downstream.
#
# The vendored `.tgz` contains the signed satellite component (see componentRef
# below). The `satellite-sig` RSASSA-PSS signature lives on this leaf component;
# any top-level wrapper component is unsigned.

{ pkgs }:

let
  inherit (pkgs) lib;

  # ── (a) SOURCE TOGGLE ─────────────────────────────────────────────────────
  #
  # The committed `.sha256sum` is the single source of truth for the blob hash.
  # It is wired in as a Nix input (see `sha256sumFile` below), so updating the file
  # automatically invalidates the derivation. When flipping to `fetchurl`, convert the
  # hex hash in that file to SRI format for the `hash` attribute.

  # Vendored (works offline today, before the GitHub repo exists):
  ctfTgz = ../vendor/hello-world-satellite-0.0.1.ctf.tgz;

  # Later — flip to GitHub publish (same sha256, nothing else changes):
  # ctfTgz = pkgs.fetchurl {
  #   url  = "https://github.com/<org>/<repo>/releases/download/v0.0.1/hello-world-satellite-0.0.1.ctf.tgz";
  #   hash = "sha256-<SRI of the vendored tgz>";
  # };

  # The RSA public key used to verify the RSASSA-PSS signature. Copy of
  # aos-host-package/.ocm/satellite.pub.pem (the private key is build-time only and
  # correctly absent). Identity must match .ocm/config: RSA/v1alpha1, RSASSA-PSS.
  pubKey = ../vendor/satellite.pub.pem;

  # The signed component version inside the CTF, and the signature name from .ocm/config.
  componentRef = "github.tools.sap/autonomous-operations/aos-demo/satellites/hello-world-satellite:0.0.1";
  signatureName = "satellite-sig";

  # sha256sum(1) file for the CTF tarball. Declared as a Nix path input so that
  # changing it invalidates the derivation. The hash is read in buildPhase (GATE 1).
  sha256sumFile = ../vendor/hello-world-satellite-0.0.1.ctf.tgz.sha256sum;

in
# ── (b) VERIFIED-UNPACK DERIVATION ──────────────────────────────────────────
pkgs.stdenvNoCC.mkDerivation {
  pname = "satellite-ctf-verified";
  version = "0.0.1";

  # `src` is the raw signed bytes (vendored or fetched). Content-addressed input.
  src = ctfTgz;
  dontUnpack = true;

  nativeBuildInputs = [ pkgs.ocm pkgs.coreutils ];

  # Keep the build hermetic: no network, no impure env. `ocm verify` is pure crypto
  # over local bytes + the embedded public key.
  buildPhase = ''
    runHook preBuild

    # ── GATE 1: byte-integrity of the tarball ───────────────────────────────
    # Read the expected hash from the vendored sha256sum file (first field, hex).
    # Fail the build if the bytes differ. On the vendored path this is the only thing
    # standing between a tampered blob and extraction, since ocm verify/download do not
    # re-hash blobs.
    expectedHash="$(awk '{print $1}' "${sha256sumFile}")"
    echo "$expectedHash  $src" | sha256sum -c - \
      || { echo "ERROR: CTF tarball sha256 mismatch (byte-integrity gate)" >&2; exit 1; }

    mkdir -p ctf
    tar -xzf "$src" -C ctf

    # The tarball unpacks to a single top-level CTF directory (named after the
    # satellite). Resolve it generically so a re-pack with a different wrapper
    # name still works.
    ctfdir="$(find ctf -maxdepth 2 -name artifact-index.json -printf '%h\n' | head -n1)"
    if [ -z "$ctfdir" ]; then
      echo "ERROR: no CTF (artifact-index.json) found in unpacked archive" >&2
      exit 1
    fi
    echo "CTF directory: $ctfdir"

    # Transient .ocmconfig embedding the public key as the RSA/v1alpha1 consumer.
    # Identity MUST carry algorithm=RSASSA-PSS + the signature name, or ocm reports
    # "missing public key, required for plain RSA signatures" (confirmed live).
    # "Plain encoding, bare public key" form per `ocm verify component-version` docs.
    # Written with no leading indentation so the YAML is unambiguous.
    cat > ocmconfig.yaml <<EOF
type: generic.config.ocm.software/v1
configurations:
  - type: credentials.config.ocm.software
    consumers:
      - identity:
          type: RSA/v1alpha1
          algorithm: RSASSA-PSS
          signature: ${signatureName}
        credentials:
          - type: Credentials/v1
            properties:
              public_key_pem_file: ${pubKey}
EOF

    # THE GATE. Non-zero exit here fails the whole Nix build.
    #   - tampered bytes that still pass the sha256 → signature mismatch → fail
    #   - stripped/absent signature → "missing signature" → fail
    echo ">>> ocm verify component-version (${signatureName}, RSASSA-PSS) ..."
    ocm --config ocmconfig.yaml verify component-version \
      --signature ${signatureName} \
      "$ctfdir//${componentRef}"

    # On success, publish the verified CTF directory as the output.
    mkdir -p "$out"
    cp -r "$ctfdir"/. "$out/"

    runHook postBuild
  '';

  dontInstall = true;

  # Record provenance for downstream derivations and humans.
  passthru = {
    inherit componentRef signatureName;
  };

  meta = with lib; {
    description = "OCM CTF for embedded satellite, signature-verified at build time";
    platforms = platforms.all;
  };
}
