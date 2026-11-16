
# Run the satellite on Ubuntu with Nix

This walkthrough brings up the signature-verified AOS satellite and its Apache Doris
datastore **inside an Ubuntu container**, using **Nix** to fetch and build every
component. It is meant for colleagues who want to review and run the stack without the
full NixOS image — on Linux, macOS (Docker Desktop / Colima), or Windows (WSL2 / Docker
Desktop).

You only need **Docker**. Everything else (Nix, OCM, the satellite, Doris, the JDK) is
installed step by step below.

> Everything runs in a long-lived container named `aos-nix`. You start it on the host
> (Step 1), then open a shell **into** it (Step 2) and run the rest from there. A few
> file-copy steps run back on the host — those are flagged explicitly.

## Why this base image

We want the satellite and Doris to run under **systemd**, exactly as they would on a real
server (`systemctl start`, the generated units, the `aos-ctl.sh install` path). The official
`ubuntu` / `debian` / `fedora` container images deliberately **strip systemd**, so they
can't boot it as PID 1 without a custom Dockerfile. We therefore use a community image
that packages a stock Ubuntu userland **with a working systemd**:
`ghcr.io/fauust/docker-systemd:ubuntu-24.04`. It is for local testing only.

## Choose an architecture

Set these once; every later command reuses them. Pick the pair for your machine:

```bash
# Apple Silicon (native, fastest):
export ARCH_PLATFORM=linux/arm64 ; export NIX_SYSTEM=aarch64-linux

# x86_64 laptop/server, OR the "faithful" path on Apple Silicon (emulated, slower):
export ARCH_PLATFORM=linux/amd64 ; export NIX_SYSTEM=x86_64-linux
```

- **amd64** matches the NixOS appliance image and the best-supported Apache Doris build.
  On Apple Silicon it runs under emulation (slower, but fewer surprises at the Doris step).
- **arm64** is native and fast on Apple Silicon; Doris ships an arm64 build too.

## Step 1 — Start an Ubuntu + systemd container

systemd must run as PID 1, which needs a few container flags (a writable cgroup mount,
its own cgroup namespace, and tmpfs for `/run`):

```bash
docker run -d --platform "$ARCH_PLATFORM" --name aos-nix \
  --privileged \
  --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /run --tmpfs /run/lock \
  ghcr.io/fauust/docker-systemd:ubuntu-24.04
```

Give systemd a few seconds, then confirm it booted and `systemctl` works:

```bash
sleep 8
docker ps --filter name=aos-nix --format '{{.Names}}\t{{.Status}}'   # Up …
docker exec aos-nix ps -p 1 -o comm=                                 # systemd
docker exec aos-nix systemctl is-system-running                      # running OR degraded (both fine)
docker exec aos-nix uname -m                                         # x86_64 / aarch64
```

`degraded` is normal in a container — a couple of host-oriented units fail to start, but
the system is booted and unit management works, which is all we need.

To start over at any point: `docker rm -f aos-nix` and repeat this step.

> **Apple Silicon note.** Building Nix inside an **emulated amd64** container fails at the
> `setup_default_profile` step (`getting pseudoterminal attributes: Function not
> implemented`) — QEMU doesn't implement every syscall Nix needs. On Apple Silicon use
> **arm64** (native) for this walkthrough. amd64 works natively on real x86 hardware.

## Step 2 — Open a shell inside the container

The rest of this walkthrough runs **from inside** `aos-nix`. Open an interactive shell
now and stay in it for every step that follows:

```bash
docker exec -it aos-nix bash
```

Your prompt is now the container's `root@…#`. Commands below are typed at that prompt —
no `docker exec` prefix. The two architecture variables you exported on the host do
**not** carry in, so re-set `$NIX_SYSTEM` here to match the container you started:

```bash
# inside the container — pick the one matching Step 1's $ARCH_PLATFORM:
export NIX_SYSTEM=aarch64-linux    # linux/arm64
# export NIX_SYSTEM=x86_64-linux   # linux/amd64
uname -m                           # sanity check: aarch64 / x86_64
```

> **Getting back in.** If you exit the shell (or open a second terminal), just run
> `docker exec -it aos-nix bash` again and re-`export NIX_SYSTEM=…`. The container keeps
> running in the background between shells. A handful of steps still run **on the host**
> (copying files in with `docker cp`); those are called out explicitly and keep their
> `docker …` form.

## Step 3 — Install the Nix-installer prerequisites

The image is a minimal Ubuntu; the Determinate Nix installer needs a few tools to fetch
and verify itself:

```bash
apt-get update -qq && DEBIAN_FRONTEND=noninteractive \
  apt-get install -y curl xz-utils ca-certificates
```

## Step 3 — Install Nix

Install Nix with the [Determinate Systems installer](https://install.determinate.systems).
The container runs real systemd, so the installer wires the Nix daemon as a systemd
service and enables flakes by default:

```bash
curl --proto '=https' --tlsv1.2 -sSfL https://install.determinate.systems/nix \
  | sh -s -- install linux --no-confirm
```

The installer edits the shell profile, but your current shell was started before that, so
`nix` is not on `PATH` yet. Source the profile to load it into **this** shell:

```bash
. /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh
```

Confirm the daemon is up with flakes enabled:

```bash
nix --version && nix store info && nix flake --help >/dev/null && echo flakes-ok
```

> Every new shell you open into the container (`docker exec -it aos-nix bash`) is a
> non-login shell and needs that same `. …/nix-daemon.sh` line before `nix` works. A fresh
> **login** shell (`bash -l`) sources it automatically. Re-set `$NIX_SYSTEM` too.

## Step 4 — Build the signature-verified satellite

This flake lives on your host, so copy it into the container first. Run the `docker cp`
**on the host** (open a second terminal, or `exit` and come back), then build from inside:

```bash
# On the HOST, from the repo root:
docker exec aos-nix mkdir -p /work
docker cp tools/nixpkg aos-nix:/work/nixpkg
```

```bash
# Back INSIDE the container:
cd /work/nixpkg
nix build ".#packages.$NIX_SYSTEM.satellite" --print-out-paths
```

> **Why copy instead of a git checkout?** The signed CTF tarball and public key live under
> `vendor/`, which is **git-ignored** (we don't commit binaries). A flake built from the
> git tree can't see ignored files, so the build would fail with `vendor/… does not exist`.
> Copying the directory in with `docker cp` (or a bind mount) gives Nix the real
> files. On the host you can build the same way with `nix build 'path:/abs/path/to/tools/nixpkg#…'` —
> a `path:` ref copies the whole directory, ignored files included.

The build runs the whole supply chain: it fetches the pinned **OCM 0.15.0** CLI, checks
the CTF's sha256, runs **`ocm verify component-version`** against the RSASSA-PSS public
key (a bad or missing signature **fails the build**), then extracts the arch-matched
binary. The result is a static Go executable:

```bash
B=$(nix build ".#packages.$NIX_SYSTEM.satellite" --print-out-paths --no-link)
file "$B"/bin/f5-vlan-demo-satellite
timeout 5 "$B"/bin/f5-vlan-demo-satellite --help || true
```

You'll see it start, log JSON, and then retry connecting to **Doris on port 9030** before
giving up — expected, since Doris isn't running yet. That's Step 5.

### Inspecting the encoded dependency tree

Nix encodes every package's full dependency tree in the flake, and you can query it on a
built result. Two different trees matter:

**Runtime closure** — exactly what must be present to *run* the package:

```bash
B=$(nix build ".#packages.$NIX_SYSTEM.satellite" --print-out-paths --no-link)
nix path-info -r "$B"
```

For the satellite this prints **one path — itself**. It is a statically-linked Go binary
with zero runtime dependencies, so its closure is just the binary. Nothing else is needed
at runtime. (This is why there is no "bundle the satellite with its deps" step: there are
no runtime deps to bundle.)

**Build-time tree** — everything Nix needs to *produce* the package (the OCM CLI, the
signature-verify gate, the whole compiler toolchain). Much larger:

```bash
drv=$(nix path-info --derivation ".#packages.$NIX_SYSTEM.satellite")
nix-store -q --tree "$drv" | head -40
```

The satellite→Doris relationship is **not** in either tree: the satellite talks to Doris
over a TCP socket at runtime, so Doris is a peer *service*, not a package dependency. That
relationship is encoded in the orchestration layer instead — the systemd units generated
by `.#satellite-systemd` (Step 5), where `aos-satellite.service` is ordered after the Doris
units, and, for the appliance, the NixOS system config in `tools/nixos`. Package deps live
in the `.nix` files; "these services run together" lives in the units.

## Step 5 — Build and run Apache Doris

### 5a. Build the Doris package

```bash
nix build ".#packages.$NIX_SYSTEM.doris" --print-out-paths
```

This fetches the pinned Doris 4.1.5 tarball and autopatchelf's its FE (Java) and BE (C++)
binaries to run under Nix's glibc.

> **The Doris tarball is large (~4.4 GB) and only distributed via the Aliyun CDN** — there
> is no Apache mirror. That endpoint can be very slow. If the build stalls on the
> download, fetch the tarball **on your host** (a faster link) and inject it into the
> container's store so the fixed-output fetch is satisfied without re-downloading:
>
> ```bash
> # On the HOST — the arm64 or x64 file matching your $NIX_SYSTEM:
> curl -LO https://apache-doris-releases.oss-accelerate.aliyuncs.com/apache-doris-4.1.5-bin-arm64.tar.gz
> docker cp apache-doris-4.1.5-bin-arm64.tar.gz aos-nix:/tmp/
> ```
> ```bash
> # INSIDE the container:
> nix store prefetch-file --hash-type sha512 \
>   --expected-hash sha512-AKwYGV/iMmgnZ1fP0W46SQ5tx0cDSMVTpbr4+TOqDnzYTHoB++j7PVghaGz/ByQq70BDbFU+vTtFmco7SbNdPw== \
>   file:///tmp/apache-doris-4.1.5-bin-arm64.tar.gz
> ```
> (That SRI hash is the arm64 one pinned in `nix/doris.nix`; use the x64 hash for amd64.)
> Re-run the build afterward — it skips the download.

### 5b. Build the systemd installer and install the services

```bash
nix build ".#packages.$NIX_SYSTEM.satellite-systemd" -o /tmp/satellite-systemd
/tmp/satellite-systemd/aos-ctl.sh install
```

`aos-ctl.sh install` creates the `aos-satellite` user, drops the
`vm.max_map_count=2000000` sysctl, symlinks **four** units into `/etc/systemd/system`, and
`systemctl enable --now`s them in order: `doris-fe`, `doris-be`, `doris-register-be`, then
`aos-satellite`. The Doris units carry absolute `/nix/store` paths and set everything Doris
needs on a non-NixOS host:

- `JAVA_HOME` → Nix `jdk17`, `LD_LIBRARY_PATH` → Nix glibc (so BE finds its NSS modules),
- `TZDIR` → Nix `tzdata` (BE aborts with *"Cannot find system tzfile"* without it),
- `StateDirectory=doris/{fe,be}` (systemd must create `/var/lib/doris/{fe,be}` *before*
  the unit's `WorkingDirectory`, or it crash-loops with `200/CHDIR`),
- `priority_networks` derived from the real route (never loopback — a loopback BE can't be
  reached by the FE's DNS cache and reports zero capacity),
- `RestrictAddressFamilies=…AF_NETLINK` (BE's `getifaddrs()` needs it).

The `aos-satellite.service` unit runs the verified satellite binary as the `aos-satellite`
user, hardened the same way as the NixOS module (dedicated user, seccomp allowlist, no
capabilities). It is ordered `After=doris-register-be.service`, so Doris is a **service
dependency** of the satellite — one `systemctl start` brings the whole stack up in order.
(Doris is *not* a Nix-package dependency: the satellite is a static binary that talks to
Doris over TCP, so its closure stays just itself.)

> `install` runs `systemctl enable --now`, which also *starts* the services; the
> `doris-register-be` oneshot waits for the FE to accept connections, and `aos-satellite`
> starts after it. On first boot the FE takes ~1–2 min to initialize before the chain
> completes. The satellite's own DB-retry loop (plus `Restart=on-failure`) is the real
> readiness gate.

### 5c. Confirm the cluster is healthy

```bash
systemctl is-active doris-fe doris-be
MYSQL=$(ls /nix/store/*-mariadb-client-*/bin/mysql | head -1)
"$MYSQL" -h127.0.0.1 -P9030 -uroot -e "SHOW BACKENDS\G"
```

A healthy single-node cluster shows the BE with `Alive: true` and a non-zero
`AvailCapacity`:

```
BackendId: 1794816921471
     Host: 172.17.0.2          # the routable IP, NOT 127.0.0.1
    Alive: true
AvailCapacity: 67.705 GB
   ErrMsg:
```

To remove everything later: `/tmp/satellite-systemd/aos-ctl.sh uninstall`.

## Step 6 — Confirm the satellite is running

The installer already started `aos-satellite.service` (it is ordered after Doris, so it
came up once the cluster formed). Check it like any other service:

```bash
systemctl is-active aos-satellite
journalctl -u aos-satellite --no-pager -n 12
```

The journal ends with the satellite's components coming up:

```
"msg":"running","component":"satellite","collectors":1,"observers":1,"operators":1
```

> On this older in-container systemd you may see
> `System call ~@privileged is not known, ignoring` — harmless; that systemd version
> doesn't recognize those seccomp group names and degrades gracefully. A current
> NixOS/systemd applies them.

That's the whole stack: a signature-verified satellite built from an OCM component, its
Apache Doris datastore, and the portable systemd integration — the satellite and all three
Doris units managed by `systemctl`, all produced by `nix build` from this one flake,
running on a plain Ubuntu host.
