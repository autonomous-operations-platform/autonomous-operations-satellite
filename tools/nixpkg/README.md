# AOS Satellite — package layer (`tools/nixpkg`)

This flake packages the Autonomous Operations (AOS) satellite and its dependencies as
reusable Nix outputs. The satellite ships as a cryptographically signed OCM artifact, and
the build verifies that signature before it packages anything.

This is the **portable** layer: it builds on any machine with Nix, and the Doris
integration can run on **any systemd Linux** (not only NixOS) via a generated installer.
The bootable **NixOS appliance image** is a separate layer that consumes this flake — see
[`../nixos`](../nixos).

> New to Nix? Terms like *flake*, *closure*, *derivation*, and *store* appear below.
> [This short intro](https://www.youtube.com/watch?v=5D3nUU1OVx8) covers them. For a
> hands-on run on a plain Ubuntu host, start with
> [`docs/run-on-ubuntu.md`](docs/run-on-ubuntu.md).

---

## What this builds

| Flake attribute | What it is |
| --- | --- |
| `packages.<system>.ctf-verified` | Unpacks the OCM CTF in a **sha256-gated + `ocm verify` signature-gated** way at build time. An independently buildable security gate. |
| `packages.<system>.satellite` | The payload binary, extracted from the *verified* CTF. `default`. |
| `packages.<system>.doris` | Apache Doris (FE + BE), patched from the upstream release tarball for Nix. |
| `packages.<system>.doris-systemd` | Portable systemd units + a `doris-ctl.sh install/uninstall` script, so Doris runs on any systemd Linux with Nix. |
| `apps.<system>.default` | `nix run` entry point → runs the verified satellite. |
| `nixosModules.default` | Hardened systemd service for the satellite. Self-sufficient: `enable = true` is enough. |
| `nixosModules.doris` | NixOS module for the Doris FE + BE services. |
| `devShells.default` | `ocm` + `doris` on `PATH`. |

`<system>` is `aarch64-linux` or `x86_64-linux`. CTF is the OCM Common Transport Format,
the on-disk layout of the signed artifact.

The satellite, Doris, and the two consumers of the Doris runtime logic relate like this —
the seed/derive/register shell logic lives **once** in `scripts/doris-*.sh` (wrapped by
`nix/doris-scripts.nix`) and is shared by both the NixOS module and the portable installer:

```
            scripts/doris-*.sh   (single source of truth)
                     │
        ┌────────────┴────────────┐
   doris-module.nix          doris-systemd.nix
   (NixOS module;            (portable units + doris-ctl.sh;
    used by ../nixos image)   used on any systemd Linux)
```

## File structure

```
tools/nixpkg/
├── flake.nix              # Packages, apps, NixOS modules, dev shell.
├── flake.lock             # Pins every input by hash (a supply-chain control). Commit this.
├── .mise.toml             # Package build tasks + pinned toolchain (ocm CLI, Nix builder image).
├── nix/
│   ├── satellite-ctf.nix      # Unpacks the signed artifact and verifies its signature at build time.
│   ├── satellite-package.nix  # Extracts the satellite binary from the verified artifact.
│   ├── module.nix             # NixOS module: the hardened satellite systemd service.
│   ├── doris.nix              # Packages Apache Doris from the upstream release tarball.
│   ├── doris-module.nix       # NixOS module: the Doris FE and BE services.
│   ├── doris-scripts.nix      # Wraps scripts/doris-*.sh (single source of Doris runtime logic).
│   ├── doris-systemd.nix      # Portable systemd units + doris-ctl.sh installer.
│   └── ocm.nix                # Pins the OCM CLI used by the build.
├── scripts/doris-*.sh     # The Doris runtime logic (seed / derive-netaddr / set-priority-networks / register).
├── docs/run-on-ubuntu.md  # Step-by-step: run the whole stack on a plain Ubuntu host with Nix.
└── vendor/                # The signed satellite artifact and its public key (git-ignored; see below).
```

---

## Artifact source: `vendor/` today, GitHub later

The build reads the signed satellite artifact from the local `vendor/` directory today.
This keeps the build fully offline while the artifact has no public home yet.

The source is a **one-line toggle** in `nix/satellite-ctf.nix`. When the artifact moves to
a GitHub release, switch from the vendored file to a `fetchurl` download:

```nix
# Today — vendored, offline:
ctfTgz = ../vendor/f5-vlan-demo-satellite-0.0.3.ctf.tgz;

# Later — a published GitHub release:
# ctfTgz = pkgs.fetchurl {
#   url  = "https://github.com/<org>/<repo>/releases/download/v0.0.3/f5-vlan-demo-satellite-0.0.3.ctf.tgz";
#   hash = "sha256-<SRI of the vendored tgz>";
# };
```

Both paths pin the **same** `sha256`, so the downstream build is byte-identical and the
switch is invisible to everything else. The build-time signature check is unchanged either
way, so provenance holds regardless of where the bytes come from.

> **`vendor/` is git-ignored** (no binaries in git). A flake built from the *git tree*
> can't see ignored files, so build with a `path:` ref — `nix build 'path:/abs/tools/nixpkg#satellite'`
> — which copies the whole directory, ignored files included. The `.mise.toml` tasks do
> this for you. Once the `fetchurl` toggle is flipped, `vendor/` goes away entirely.

## Supply-chain guarantees

| Gate | Mechanism | Fails build when |
| --- | --- | --- |
| Input reproducibility | `flake.lock` pins nixpkgs + all inputs by hash | an input's content changes |
| Toolchain pinning | `ocm` 0.15.0 pinned (mise + Nix FOD overlay); Nix image pinned by **digest** | the verifier/toolchain drifts |
| **Byte integrity of CTF** | fixed-output sha256 (`fetchurl` hash, or the `sha256sum -c` gate in `satellite-ctf.nix`) | tarball bytes ≠ pinned hash |
| **Provenance of satellite** | `ocm verify component-version` RSASSA-PSS vs `vendor/satellite.pub.pem` at build | signature invalid/absent |
| Output integrity | Nix content-addressed store | any derivation output tampered |

> **Why two integrity gates, not one.** `ocm verify` signs only the component
> *descriptor's* normalised form — it does **not** re-hash resource blobs, and
> `ocm download` does not either (both confirmed live with ocm 0.15.0). A byte flipped
> inside a resource blob leaves the descriptor and its signature valid. The sha256 gate
> over the whole tarball is what catches blob tampering. Provenance (`ocm verify`) and
> byte-integrity (sha256) are therefore independent and both load-bearing.

---

## Consuming this flake

### A. On an existing NixOS / Nix host

#### A1. Install only the satellite binary

```sh
nix profile install "path:tools/nixpkg"   # or a github: ref once published
nix run "path:tools/nixpkg"                # run without installing
```

Installs only the satellite executable. It does **not** install Doris or create any
systemd unit — `nix profile install` installs a *package*, never system configuration.
Building the binary still runs the full supply-chain chain (sha256 + `ocm verify`) on your
machine, so provenance is enforced at install time, not just in CI.

#### A2. Run the satellite as a hardened systemd service

Add the satellite module to a NixOS host and `nixos-rebuild switch`. The module is
self-sufficient:

```nix
{
  inputs.aos.url = "path:tools/nixpkg";   # or a github: ref once published
  outputs = { self, nixpkgs, aos, ... }: {
    nixosConfigurations.my-host = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        aos.nixosModules.default
        ({ ... }: { services.aosSatellite.enable = true; })
        ./hardware-configuration.nix
      ];
    };
  };
}
```

Installs the `aos-satellite.service` unit and its service user. Still no Doris — that is a
separate module (A3). FIPS is a whole-host decision and is **not** forced by the module.

#### A3. Add Doris

```nix
modules = [
  aos.nixosModules.default      # satellite service
  aos.nixosModules.doris        # Doris FE + BE services
  ({ pkgs, ... }: {
    services.aosSatellite.enable = true;
    services.doris.enable = true;             # doris-fe, doris-be, doris-register-be
    # services.doris.feHeap = "2048m";        # size for the host if the defaults don't fit
    # services.doris.beMemLimit = "4G";
  })
];
```

> **Doris reuses the satellite's service user.** `services.doris.user` defaults to
> `aos-satellite`, created by `nixosModules.default`. If you enable Doris without the
> satellite module, set `services.doris.user`/`.group` to a user you provide.

### B. On any systemd Linux (non-NixOS) via the portable installer

For hosts that are not NixOS but have Nix and systemd (Ubuntu, Debian, Fedora, RHEL…):

```sh
nix build 'path:tools/nixpkg#doris-systemd' -o /tmp/doris-systemd
sudo /tmp/doris-systemd/doris-ctl.sh install     # create user, drop sysctl, enable --now
# …
sudo /tmp/doris-systemd/doris-ctl.sh uninstall
```

`doris-ctl.sh` (no `make` needed) symlinks three systemd units into
`/etc/systemd/system`, drops `vm.max_map_count` via `sysctl.d`, creates the `aos-satellite`
user, and `systemctl enable --now`s FE, BE, and the register one-shot. The generated units
carry absolute `/nix/store` paths and set everything Doris needs on a non-NixOS host
(`JAVA_HOME`, `LD_LIBRARY_PATH` to Nix glibc for NSS, `TZDIR` to Nix tzdata,
`StateDirectory`, routable `priority_networks`, `AF_NETLINK`). They are host-specific —
regenerate with `nix build` per machine.

A full worked example on a plain Ubuntu host (install Nix → build → run Doris → run the
satellite) is in **[`docs/run-on-ubuntu.md`](docs/run-on-ubuntu.md)**.

---

## What to know before you work on Doris

- **Upstream binaries are patched for Nix.** `doris_be` is a generic-Linux ELF;
  `autoPatchelfHook` rewrites its interpreter and library paths to Nix-store equivalents.
- **FE and BE run from a writable copy of their home.** The upstream start scripts write
  logs and a process lock under `DORIS_HOME`, so a writable tree is seeded under
  `/var/lib/doris` rather than running from the read-only Nix store.
- **`priority_networks` is mandatory.** Without it the FE registers itself as `127.0.0.1`,
  which Doris's internal resolver cannot resolve, so the BE never reports its disks. A
  routable address is derived at boot and written into both configs.
- **BE needs `TZDIR`.** On a minimal/NixOS host there is no `/usr/share/zoneinfo`, so BE
  aborts with *"Cannot find system tzfile"* unless `TZDIR` points at Nix's tzdata.
- **A one-shot registers the BE with the FE.** Doris does not self-form a single-node
  cluster; `doris-register-be` runs `ALTER SYSTEM ADD BACKEND` once the FE is up.

## Build tasks (mise + containerized Nix)

A macOS host has no Nix. `.mise.toml` orchestrates a pinned Linux container that owns a
real `/nix` and **is** the builder. It pins the `ocm` CLI
(`github:open-component-model/open-component-model@0.15.0`) and the Nix builder image by
**digest** (`nixos/nix:2.31.2@sha256:…`).

```sh
mise run nix-shell-up    # start the pinned Nix container; confirm flakes enabled
mise run verify-ctf      # build the standalone signature-verification gate
mise run build           # nix build path:/work#packages.<system>.satellite
mise run doris-systemd   # build the portable systemd installer
```

Prerequisite: a Docker-compatible runtime driving **Linux** containers (native Docker /
Podman on Linux; Colima, Docker Desktop, Rancher, or OrbStack on macOS).

### End-to-end verification

1. `mise run nix-shell-up` — confirm `nix --version` and flakes inside the container.
2. **Negative test (the gate is load-bearing):** corrupt one CTF blob byte in a scratch
   copy → `mise run verify-ctf` must fail (sha256 gate). Restore → succeeds.
3. `mise run build` → inspect the extracted satellite binary under `result/bin`.
4. `mise run doris-systemd` → the units + `doris-ctl.sh` build; `grep /nix/store` the units.
5. For the full stack on a real host, follow [`docs/run-on-ubuntu.md`](docs/run-on-ubuntu.md).
