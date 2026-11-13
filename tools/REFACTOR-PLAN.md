# Refactor: split the Nix flake into a portable package layer (`tools/nixpkg/`) and a NixOS image layer (`tools/nixos/`)

## Context

Today `tools/nixos/` holds everything: a flake that builds both reusable **packages**
(`ocm`, the signature-verified satellite, Apache Doris) and a whole **NixOS image**
(`image.nix`, `fips.nix`, `nixosConfigurations.satellite-host`, the VM boot test). The
Doris runtime logic — the hard-won lessons about writable dirs, `priority_networks`,
AF_NETLINK, NSS resolution, BE registration — is inlined as shell strings inside the
NixOS module `nix/doris-module.nix`, so it only runs on NixOS.

Colleagues want to run the satellite + Doris on **any Linux distro that has Nix and
systemd** (Ubuntu, Debian, Fedora, RHEL…), not only by booting a full NixOS image. The
goal is a clean **package/module flake** that stands on its own, plus a **portable
systemd installer** that reuses the exact same runtime logic — so the NixOS image
becomes just one consumer of the flake, not the whole thing.

Outcome after this refactor:
- `tools/nixpkg/` — a self-contained flake exposing packages, NixOS modules, a portable
  systemd installer, apps, and a dev shell. Buildable on any Nix host.
- `tools/nixos/` — the NixOS-image-only files, consuming `tools/nixpkg/` as a flake input.
- One copy of the Doris runtime logic, as runnable/testable shell scripts, shared by both
  the NixOS module and the portable installer.

Non-goals: macOS/Windows/non-systemd Linux (systemd is required); changing Doris/OCM
versions or the supply-chain gate; the running container experiment (separate thread).

## Layer classification (decided)

**Layer 1 → `tools/nixpkg/`** (pure packages + reusable modules + portable installer):
`flake.nix` (split), `flake.lock`, `nix/ocm.nix`, `nix/satellite-ctf.nix`,
`nix/satellite-package.nix`, `nix/doris.nix`, `nix/doris-module.nix`, `nix/module.nix`,
`vendor/`, `.ocm/`, `README.md`, `.gitignore`.

**Layer 2 → `tools/nixos/`** (whole-OS image assembly):
`nix/image.nix`, `nix/fips.nix`, `nix/debug.nix`, `nix/boot-test.nix`, its own
`flake.nix` + `flake.lock`, and the image/run/boot-test/toplevel/gc `.mise.toml` tasks.

**Key coupling already verified:** `boot-test.nix` imports `module.nix` (L1) + `fips.nix`
(L2); `image.nix` builds `systemModules` from `module.nix` + `doris-module.nix` (L1) +
`fips.nix` (L2). So Layer 2 *consumes* Layer 1's exported `nixosModules`. This is the
intended consumer relationship and drives the split below.

## Approach

### Step 0 — Persist this plan in the repo

Write this plan to `tools/REFACTOR-PLAN.md` so it lives with the code and colleagues can
follow along. (First implementation action.)

### Step 1 — Create directories and move files (preserve git history)

- `git mv tools/nixos tools/nixpkg` to move the whole tree at once (keeps history for
  every file).
- Create `tools/nixos/nix/` and `git mv` the Layer-2 files back into it:
  `image.nix`, `fips.nix`, `debug.nix`, `boot-test.nix`.
- `vendor/` and `.ocm/` stay in `tools/nixpkg/` (they are inputs to
  `satellite-ctf.nix`; relative path `../vendor/...` in that file is unchanged because
  the file and `vendor/` move together).

### Step 2 — Extract the Doris runtime logic into shared shell scripts

Create `tools/nixpkg/scripts/` with real `.sh` files, one per current inline fragment in
`nix/doris-module.nix`:
- `doris-seed-home.sh` ← `seedDorisHome` (args: component, data-dir name)
- `doris-derive-netaddr.sh` ← `deriveNetAddr` (prints `IP` and `PN`)
- `doris-set-priority-networks.sh` ← `setPriorityNetworks` (args: conf path, PN)
- `doris-register-be.sh` ← the `doris-register-be` unit's `script`

Add `tools/nixpkg/nix/doris-scripts.nix`: wraps each with `pkgs.writeShellApplication`,
baking the PATH deps currently in `dorisScriptPath` (bash, coreutils, util-linux, gnused,
gnugrep, gawk, procps, iproute2) and `mariadb.client` for the register script. Returns an
attrset of wrapped script derivations. **This is the single source of truth** for the
lessons recorded in memory (`doris-fe-be-loopback-priority-networks`,
`restrictaddressfamilies-needs-netlink`, `rm-f-directory-prestart-trap`,
`doris-seed-mutable-dirs`, `autopatchelf-nss-hostname-resolution`) — keep the explanatory
comments with the scripts.

### Step 3 — Rewrite `nix/doris-module.nix` to consume the shared scripts

Replace the inline `seedDorisHome`/`deriveNetAddr`/`setPriorityNetworks`/register-`script`
text with calls to the wrapped scripts from `doris-scripts.nix`. The systemd unit
*structure* (Type, StateDirectory, PIDFile, hardening, ordering, env) is unchanged — only
the shelled-out logic is now a shared script. Behavior must stay identical.

### Step 4 — Add the portable systemd installer output

- `tools/nixpkg/nix/doris-systemd.nix`: builds a package whose output contains:
  - `doris-fe.service`, `doris-be.service`, `doris-register-be.service` — plain unit files
    with absolute `/nix/store` paths (JDK home, glibc for `LD_LIBRARY_PATH`, the wrapped
    scripts, the Doris package), generated from the same values the NixOS module uses.
  - `sysctl.d/99-doris.conf` (`vm.max_map_count = 2000000`).
  - `sysusers.d/doris.conf` (creates the `aos-satellite` user/group) — plus a fallback
    `useradd` path in the Makefile for distros whose systemd lacks sysusers.
  - A **`Makefile`** with:
    - `install`: `ln -sfn` each unit into `/etc/systemd/system/`, symlink the sysctl.d and
      sysusers.d drop-ins, create the user if missing, `sysctl --system`,
      `systemctl daemon-reload`, `systemctl enable --now doris-fe doris-be
      doris-register-be`.
    - `uninstall`: `systemctl disable --now …`, `rm` the symlinks, `daemon-reload`.
      (Symlinks, not copies, so a later `nix build` refreshes live units in place.)
- Document that this requires systemd and root for `install`/`uninstall`.

### Step 5 — Split `tools/nixpkg/flake.nix` (Layer 1 only)

Keep: `nixpkgs` input; `ocmOverlay` + `dorisOverlay`; `pkgsFor`; `chainFor`.
Keep outputs: `packages.{satellite,default,doris,ctf-verified}`, **new**
`packages.doris-systemd`; `nixosModules.{default,doris}`; `apps.{default,satellite}`;
`devShells.default`.
Remove (these move to Layer 2): `nixos-generators` input; `systemModulesFor`;
`bootBaselineModule`; `packages.{image,image-debug,image-qcow}`;
`nixosConfigurations.*`; `checks.boot-test`; the `./nix/fips.nix` reference.

### Step 5b — Validate Layer 1 in a vanilla Ubuntu container + write the walkthrough README (iterative, pause after each step)

Before building the image layer, prove the `tools/nixpkg/` flake runs the satellite on a
plain distro, and capture the exact steps as `tools/nixpkg/README.md` (or a dedicated
`tools/nixpkg/docs/run-on-ubuntu.md`). This is both a verification gate and deliverable
documentation for colleagues.

Work **one documented step at a time, pausing after each to review and iterate** before
writing the next. The target narrative:

1. Spin up a vanilla Ubuntu container (`docker run -d --platform linux/amd64 --name
   aos-nix ubuntu:24.04 sleep infinity`). Document arch choice (amd64 = faithful +
   well-supported Doris path).
2. Install prerequisites (`curl`, `xz-utils`, `ca-certificates`, `sudo`).
3. Install Nix (Determinate Systems installer, `--init none`), start the `nix-daemon`
   (no systemd → run it backgrounded), load the profile, verify `nix --version` +
   `nix store ping`.
4. Fetch/point at the flake (bind-mount the repo or `nix ... build path:...`), then
   `nix build .#satellite` → signature-verified binary; show the OCM verify in the log.
5. `nix build .#doris` and run Doris by hand via the portable installer
   (`nix build .#doris-systemd` + `make install`) OR, if the container lacks systemd,
   document the manual fallback (run the shared `scripts/doris-*.sh` + `start_fe.sh`/
   `start_be.sh` directly). Capture the `vm.max_map_count` host-kernel caveat.
6. Run the satellite against Doris; confirm it connects (MySQL protocol, port 9030).

Each numbered step is drafted, reviewed with the user, run in the live `aos-nix`
container, corrected to match reality, and only then committed to the README before
moving to the next. The README must reflect commands that actually worked, not
idealized ones.

### Step 6 — Create `tools/nixos/flake.nix` (Layer 2, consumer)

New flake with inputs `nixpkgs`, `nixos-generators`, and
`aos-nixpkg.url = "path:../nixpkg"`. Rebuild `systemModulesFor`/`bootBaselineModule`
here, importing `aos-nixpkg.nixosModules.default` and `.doris` and the satellite package
from `aos-nixpkg.packages.<system>.satellite`. Re-expose
`packages.{image,image-debug}`, `nixosConfigurations.{satellite-host,
satellite-host-debug}`, `checks.boot-test`. `boot-test.nix`/`image.nix` move here and
import `fips.nix`/`debug.nix` locally + the satellite module from the input.

### Step 7 — Split `.mise.toml`

- `tools/nixpkg/.mise.toml`: `nix-shell-up`, `build` (default `packages.<sys>.satellite`),
  `verify-ctf`, plus a `doris-systemd` build task. Keep the pinned `ocm` tool + the
  container/store-volume env.
- `tools/nixos/.mise.toml`: `image`, `run`, `boot-test`, `toplevel`, `gc` (unchanged
  logic, run from the nixos dir which now has the image flake).

### Step 8 — Update `README.md` and `TODO.md`

Split/relocate the README: packaging + portable-install instructions in
`tools/nixpkg/README.md`; image build/boot instructions in `tools/nixos/README.md`.
`TODO.md` (untracked) moves to whichever layer it best fits (likely `tools/nixos/`).

## Critical files

- Move/split: `tools/nixos/flake.nix`, `tools/nixos/.mise.toml`, `tools/nixos/README.md`.
- New in `tools/nixpkg/`: `nix/doris-scripts.nix`, `nix/doris-systemd.nix`,
  `scripts/doris-*.sh`, `Makefile` (in the doris-systemd output).
- Rewritten: `tools/nixpkg/nix/doris-module.nix` (consume shared scripts).
- Moved to `tools/nixos/`: `nix/{image,fips,debug,boot-test}.nix`, new `flake.nix`.
- Unchanged logic, just relocated: `nix/{ocm,satellite-ctf,satellite-package,doris,
  module}.nix`, `vendor/`, `.ocm/`.

## Verification

Run inside the pinned Nix builder (via `.mise.toml` tasks or the `aos-nix` container).
Prefer the native-arch system for speed; the container experiment is amd64.

1. **Flake evaluates, packages build** (`tools/nixpkg/`):
   - `nix flake check` (should no longer reference image/boot-test).
   - `nix build .#ctf-verified` — supply-chain gate still passes (signature verify).
   - `nix build .#satellite` — `result/bin/f5-vlan-demo-satellite` is an ELF for the arch.
   - `nix build .#doris` — `result/{fe,be}` present.
   - `nix build .#doris-systemd` — output has the 3 unit files, sysctl.d, sysusers.d,
     Makefile; units reference valid `/nix/store` paths (`grep /nix/store result/*.service`).
2. **Shared scripts run standalone** (proves single-source-of-truth): execute
   `scripts/doris-derive-netaddr.sh` in the container and confirm it prints a routable
   `IP`/`PN` (non-loopback), matching the behavior the NixOS module relied on.
3. **NixOS module behavior unchanged**: `nix build
   .#nixosConfigurations... ` from `tools/nixos/` still evaluates; `boot-test` still
   builds (`tools/nixos/`: `nix build .#checks.<sys>.boot-test`) — asserts FIPS + service.
4. **Portable install / vanilla-Ubuntu walkthrough end-to-end** (the new capability,
   captured as `tools/nixpkg/README.md` in Step 5b): in the amd64 `aos-nix` Ubuntu
   container with Nix + the Determinate daemon, `nix build .#doris-systemd` then
   `sudo make -C result install`; confirm `systemctl status doris-fe doris-be` reach
   active and `mysql -h127.0.0.1 -P9030 -uroot -e 'SHOW BACKENDS'` lists the BE with
   non-zero capacity. `sudo make -C result uninstall` removes the symlinks and units.
   Each step of this walkthrough is validated live and iterated on before being written
   into the README.
5. **History preserved**: `git log --follow tools/nixpkg/nix/doris-module.nix` shows
   pre-move history.

## Open risks

- The portable units bake absolute `/nix/store` paths, so they are host-specific
  (regenerate via `nix build` per machine) — expected for Nix-generated units; document it.
- `vm.max_map_count` is a **host-kernel** setting; inside Docker it must be set on the
  Colima VM/host, not just the container. The sysctl.d drop-in works on a real systemd
  host; in the container we may need `--privileged` or a host-side `sysctl`. Flag in docs.
- `doris-register-be.sh` needs `mariadb.client`'s `mysql`; ensure it's on the wrapped
  script's PATH (it is in `writeShellApplication` inputs), not assumed from the system.
