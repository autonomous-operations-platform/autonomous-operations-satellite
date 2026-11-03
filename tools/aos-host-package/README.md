# AOS Satellite Host Package

A NixOS image that packages a **signature-verified OCM artifact**,
distributable as a **flake**, with supply-chain security. Per [ADR-024](https://pages.github.tools.sap/autonomous-operations/autonomous-operations/#/architecture/specifications/spec-ao/adrs/adr-024): the host OS
and the satellite package ship together as one Nix closure, built and switched offline
from a local substituter (air-gapped), with atomic generation-based rollback. Hence, OS version
and satellite version are tightly coupled.

> [!TIP]
> A lot of the terms in this README are highly specific to Nix and NixOS.
> If you are unfamiliar, get acquaninted with the most basic terms in [this video](https://www.youtube.com/watch?v=5D3nUU1OVx8)

## What this builds

| Flake attribute | What it is |
| --- | --- |
| `packages.<system>.ctf-verified` | Unpacks the OCM CTF in a **sha256-gated + `ocm verify` signature-gated** manner at build time (step 1). Independently buildable security gate. |
| `packages.<system>.satellite` | Sets up the payload binary, extracted from the *verified* CTF (step 2). `default`. |
| `apps.<system>.default` | `nix run` entry point → runs the verified satellite. |
| `nixosModules.default` | Hardened systemd service for the satellite (step 3). Self-sufficient: `enable = true` is enough. |
| `nixosConfigurations.satellite-host` | The air-gapped `nixos-rebuild switch` toplevel closure; boots with `fips=1` (step 4). |
| `packages.<system>.image` | Generates a VM image using `nixos-generators`. Default **qcow**; Output is configurable by attr (`image-raw-efi` / `image-qcow` / `image-qcow-efi` / `image-iso`) |
| `checks.<system>.boot-test` | NixOS VM test: boots the host, asserts **FIPS active** + hardened service running (ADR-024). |
| `devShells.default` | `ocm` + `nixos-generators`. |

`<system>` specifies the architecture. Either `aarch64-linux` (native on the Colima VM) or `x86_64-linux` (binfmt/QEMU).

## Consuming this flake from GitHub

Once this tree is a GitHub repo (say `github:org/aos-host-package`), there are two ways
to consume it, and both work from the same flake.

### A. Use the package on an existing NixOS / Nix distribution

The verified satellite is `packages.<system>.default`, so:

```sh
# Install the verified satellite binary into your Nix profile:
nix profile install github:org/aos-host-package

# …or just run it without installing:
nix run github:org/aos-host-package
```

Both resolve to the signature-verified build for *your* machine's system. Building it
runs the same two gates (sha256 byte-integrity + `ocm verify` RSASSA-PSS) on your box —
provenance is enforced at install time, not just in CI. This needs network (GitHub +
`cache.nixos.org`) or a configured substituter; on an air-gapped host, populate the
local substituter first (that is the ADR-024 path).

To run it as the **hardened systemd service** (dedicated user, seccomp, dropped caps)
rather than an ad-hoc binary, add the module to your `configuration.nix` — it is
self-sufficient, so `enable = true` is all it needs:

```nix
{
  inputs.aos.url = "github:org/aos-host-package";

  outputs = { self, nixpkgs, aos, ... }: {
    nixosConfigurations.my-host = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        aos.nixosModules.default
        ({ ... }: {
          services.aosSatellite.enable = true;   # package defaults to the verified build
          # Optional: pin a prebuilt closure instead (air-gapped / local substituter):
          # services.aosSatellite.package = <prebuilt>;
        })
        ./hardware-configuration.nix
      ];
    };
  };
}
```

`services.aosSatellite.package` defaults to the verified satellite built for the host's
own `pkgs.system`, so you don't have to replicate the pinned-OCM overlay wiring. FIPS
(`boot.kernelParams = [ "fips=1" ]`) is **not** forced by the module — it is a
whole-host decision; set it in your own config if you want it (as
`nixosConfigurations.satellite-host` does).

### B. Build the combined image / A/B-switch closure (ADR-024 delivery)

```sh
nix build github:org/aos-host-package#image          # qcow (or #image-raw-efi / #image-iso)
nix build github:org/aos-host-package#nixosConfigurations.satellite-host.config.system.build.toplevel
```

This is the combined OS+satellite closure: `nixos-rebuild switch --flake` it onto the
target for atomic, generation-based rollback, or deploy the image directly.

> **A vs. B.** B couples the satellite version to the OS generation (ADR-024 combined
> delivery). A lets an *existing* fleet machine pull just the verified satellite without
> re-imaging — same provenance gates, looser coupling. Use B for the appliance; use A to
> run the satellite on a host someone else manages.

## Supply-chain guarantees

| Gate | Mechanism | Fails build when |
| --- | --- | --- |
| Input reproducibility | `flake.lock` pins nixpkgs + all inputs by hash | an input's content changes |
| Toolchain pinning | `ocm` 0.15.0 pinned (mise + Nix FOD overlay); Nix image pinned by **digest** | the verifier/toolchain drifts |
| **Byte integrity of CTF** | fixed-output sha256 (`fetchurl` hash, or the `sha256sum -c` gate in `satellite-ctf.nix` on the vendored path) | tarball bytes ≠ pinned hash |
| **Provenance of satellite** | `ocm verify component-version` RSASSA-PSS vs `vendor/satellite.pub.pem` at build | signature invalid/absent |
| Output integrity | Nix content-addressed store | any derivation output tampered |

> **Why two integrity gates, not one.** `ocm verify` signs only the component
> *descriptor's* normalised form — it does **not** re-hash resource blobs, and
> `ocm download` does not either (both confirmed live with ocm 0.15.0). A byte flipped
> inside a resource blob leaves the descriptor and its signature valid. The sha256 gate
> over the whole tarball (the `fetchurl` hash on the GitHub path, or the explicit
> `sha256sum -c` in `nix/satellite-ctf.nix` on the vendored path) is what catches blob
> tampering. Provenance (`ocm verify`) and byte-integrity (sha256) are therefore
> independent and both load-bearing.

## Build environment (mise + containerized Nix)

This Mac has no Nix. mise orchestrates a pinned Linux container that owns a real `/nix`
and **is** the Linux builder — NixOS images build natively inside it (arm64 on the
running Colima VM), so no Darwin `linux-builder` cross-build machinery is needed.

`.mise.toml` pins:
- the `ocm` CLI (`github:open-component-model/open-component-model@0.15.0`) for Mac-side
  verify/inspect helpers — matching the in-container verifier;
- the Nix builder image **by digest** (`nixos/nix:2.31.2@sha256:29fc5fe…`), itself a
  supply-chain control for the build toolchain.

Tasks (wrap the container against `/work` with a persistent `aos-nix-store` Docker volume):

```sh
mise run nix-shell-up    # start the pinned Nix container; confirm flakes enabled
mise run verify-ctf      # build the standalone signature-verification gate
mise run build           # nix build .#packages.<system>.satellite
mise run toplevel        # the air-gapped nixos-rebuild switch closure
mise run image           # the NixOS image (qcow by default; pass image-iso to override)
```

Prerequisite: the Colima Docker host must be running (`colima status`).

## The OCM source toggle (vendored → GitHub)

`nix/satellite-ctf.nix` has a one-line source binding. Today it reads the vendored
tarball (offline, pre-publish). Both paths are fixed-output and pinned by the **same
sha256**, so everything downstream is byte-identical:

```nix
# vendored (works offline today):
ctfTgz = ../vendor/hello-world-satellite-0.0.3.ctf.tgz;

# later — flip to GitHub publish (same sha256, nothing else changes):
# ctfTgz = pkgs.fetchurl {
#   url  = "https://github.com/<org>/<repo>/releases/download/v0.0.3/hello-world-satellite-0.0.3.ctf.tgz";
#   hash = "sha256-Qi8rrxZCqqjJAzZ6G0zcyGcUOxGAW3BvWlOFYXl2G20=";
# };
```

The build-time `ocm verify` is unchanged across the switch — provenance is enforced
regardless of source.

## The signed artifact

The vendored `.tgz` is the signed OCM component
`github.tools.sap/autonomous-operations/aos-demo/satellites/hello-world-satellite:0.0.3`
(signature `satellite-sig`, RSASSA-PSS, Plain encoding — mirrors `.ocm/config`). It
carries the `hello-world-satellite` payload resource with `linux/{amd64,arm64}` blobs;
the build extracts the blob matching the target system.

> The `satellite` *runtime* (component `aos-demo:0.0.2`, resource `satellite`) lives in a
> **separate, currently-unsigned** CTF that is not part of the signed tarball. When that
> component is signed and published it can be added here as a second verified source +
> extraction, identically. For now the package ships the signed payload the supply-chain
> gate actually covers.

## FIPS (ADR-024)

FIPS is wired by `nix/fips.nix` (imported into `nixosConfigurations.satellite-host` and
the VM boot test) as **two separable pieces**, because one without the other is a trap:

1. **`boot.kernelParams = [ "fips=1" ]`** — asks the kernel to enter FIPS mode at boot.
   On by default (`services.aosSatellite.fips.enable`).
2. **A kernel rebuilt with `CONFIG_CRYPTO_FIPS`** — what actually makes `fips=1` take
   effect. **Opt-in, OFF by default** (`services.aosSatellite.fips.rebuildKernel`),
   because it forces a full kernel compile.

> **`fips=1` alone is inert.** The stock nixpkgs kernel has no `CONFIG_CRYPTO_FIPS`, so
> the param is silently ignored and `/proc/sys/crypto/fips_enabled` never appears — caught
> by the VM boot test (the service booted fine; the FIPS assertion failed with "No such
> file or directory"). Enabling `CONFIG_CRYPTO_FIPS` on Linux 6.12 also requires
> `CRYPTO_DRBG`, `MODULE_SIG`, and leaving the crypto self-tests on — all handled in
> `nix/fips.nix`.

**As shipped, `fips.rebuildKernel` is OFF**, so `nixosConfigurations.satellite-host` sets
`fips=1` on the stock kernel and **FIPS is NOT active at runtime**. This is deliberate:
the kernel compile is slow (and painfully slow under QEMU/TCG on a dev Mac), and the real
FIPS kernel belongs on the actual appliance target — ideally an `x86_64` / KVM builder
where the compile and VM boot are fast.

**To get real FIPS**, set it on the appliance build and verify:

```nix
services.aosSatellite.fips.rebuildKernel = true;   # triggers the kernel compile
```

```sh
mise run boot-test   # VM test; it enables rebuildKernel and asserts fips_enabled == 1
# or: nix build .#checks.<system>.boot-test
```

Status here: the kernel **config** was validated to build with `CRYPTO_FIPS=y` and its full
dependency chain (cheap, no compile). The booted runtime assertion
(`fips_enabled == 1`) is **not run in this environment** — it needs the full kernel
compile + VM boot, deferred to a KVM-capable builder.

Remaining note for real hardware:
- With a separate `/boot`, FIPS integrity self-checks may also need `boot.initrd` module
  signing / `rd.fips` wiring — verify on the target.

## Publishing (when the OCM artifact goes to GitHub)

1. `ocm transfer` the signed component to `ghcr.io`, and/or attach the `.ctf.tgz` to a
   GitHub Release.
2. Flip the `nix/satellite-ctf.nix` source binding from vendored to `fetchurl`; the
   committed `flake.lock` + the pinned sha256 keep it reproducible.
3. The build-time `ocm verify` is unchanged — provenance is enforced regardless of source.

## Repository notes

This tree is not yet a git repository. When it becomes one: **commit `flake.lock`**
(input pinning is a supply-chain gate), and consider tracking the ~5 MB
`vendor/*.ctf.tgz` via **git-lfs** — it will be replaced by `fetchurl` once the artifact
is GitHub-published.

## End-to-end verification

1. `mise run nix-shell-up` — confirm `nix --version` and flakes inside the container.
2. **Negative test (the gate is load-bearing):** corrupt one CTF blob byte in a scratch
   copy → `mise run verify-ctf` must fail (sha256 gate). Restore → succeeds. *(Verified:
   corrupting a blob fails the `sha256sum -c` byte-integrity gate; the untouched artifact
   passes both gates and logs `SIGNATURE VERIFICATION SUCCESSFUL`.)*
3. `mise run build` → inspect `result/bin/hello-world-satellite`. *(Verified: extracts the
   arm64 payload and runs the collector/observer/operator pipeline.)*
4. `mise run toplevel` — the air-gapped switch closure builds. *(Verified: builds; the
   system has `fips=1`, and `aos-satellite.service` runs the verified package as an
   unprivileged, seccomp-filtered, cap-dropped user.)*
5. `mise run image` — the combined OS+satellite image builds. *(Verified: `iso` builds
   with no extra flags; `qcow` builds via the `kvm` system-feature → a 2.4 GB
   `nixos.qcow2`.)*
6. `mise run boot-test` — NixOS VM test: boots the host (with `fips.rebuildKernel`
   enabled) and asserts **FIPS active** (`/proc/sys/crypto/fips_enabled == 1`) + service
   running. *(The FIPS kernel **config** is validated to build with `CRYPTO_FIPS=y` + its
   full dependency chain; the booted assertion needs the kernel compile + VM boot and is
   deferred to a KVM-capable builder. This test is what caught that a bare `fips=1` is
   inert on the stock kernel.)*
7. `nix flake check` / committed `flake.lock` — flake is consistent; all inputs pinned.
