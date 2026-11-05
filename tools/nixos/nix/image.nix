# image.nix — host-side disk image via systemd-repart (NO nested QEMU VM)
#
# WHY THIS EXISTS (the thing that was eating 40 minutes):
#   nixos-generators' `qcow`/`qcow-efi` formats call nixpkgs `make-disk-image.nix`,
#   which — for any *bootable* image — boots a nested QEMU VM to lay down the
#   filesystem and install the bootloader (`buildImage = vmTools.runInLinuxVM …`).
#   On Apple Silicon there is no nested KVM, so that inner VM runs under TCG software
#   emulation. Copying a 7.7 GiB closure through an emulated CPU is what took ~40 min.
#   No flag on make-disk-image.nix avoids the VM (onlyNixStore disallows a bootloader).
#
#   `image.repart` (the NixOS `modules/image/repart.nix` module, imported explicitly
#   below) builds the image with `systemd-repart` running as plain host-side userspace
#   (`unshare --map-root-user fakeroot systemd-repart …`). No QEMU, no TCG, no VM —
#   the build is bounded by copying the closure, i.e. minutes instead of ~40.
#
#   Because repart's builder calls `unshare`, the `mise` image tasks run the container
#   with `--security-opt seccomp=unconfined` (Docker's default seccomp blocks unshare)
#   and `nix ... --option sandbox false` (so unshare is not nested inside Nix's own
#   sandbox namespace). See the comments in .mise.toml [tasks.image].
#
# OUTPUT: repart emits a `.raw` image. `mise run run` boots a qcow2, so we convert
#   raw → qcow2 host-side with `qemu-img convert` (still no VM). The result is a
#   derivation directory containing `${name}.qcow2`, matching the `ls *.qcow2` the
#   run task does.
#
# BOOT: UEFI + Unified Kernel Image (UKI) on the ESP — the module's own known-good
#   shape. aarch64 `-machine virt` under QEMU/EDK2 (what `mise run run` already uses)
#   boots this directly.

{ nixpkgs, system, systemModules }:

let
  repartModule = { config, lib, pkgs, modulesPath, ... }: {
    # In nixpkgs 25.05 the repart module is NOT in the default module list (it is only
    # referenced from documentation.nixos.extraModules), so import it explicitly to
    # make the `image.repart` options available.
    imports = [ "${toString modulesPath}/image/repart.nix" ];

    # systemd in the initrd is the supported path for repart/UKI images.
    boot.initrd.systemd.enable = true;

    # UKI + systemd-boot: the loader entries come from systemd-boot; the kernel+initrd
    # ride in a single UKI placed on the ESP below. The image build cannot touch NVRAM.
    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = false;

    # Root is found by the GPT partition label written by repart ("nixos"), so no
    # hardware-specific device nodes are baked in.
    fileSystems."/" = {
      device = "/dev/disk/by-label/nixos";
      fsType = "ext4";
    };

    image.repart = {
      name = "satellite-host";
      partitions = {
        "10-esp" = {
          contents = {
            "/EFI/BOOT/BOOTAA64.EFI".source =
              "${pkgs.systemd}/lib/systemd/boot/efi/systemd-bootaa64.efi";
            "/EFI/Linux/${config.system.boot.loader.ukiFile}".source =
              "${config.system.build.uki}/${config.system.boot.loader.ukiFile}";
          };
          repartConfig = {
            Type = "esp";
            Format = "vfat";
            SizeMinBytes = "512M";
          };
        };
        "20-root" = {
          storePaths = [ config.system.build.toplevel ];
          repartConfig = {
            Type = "root";
            Format = "ext4";
            Label = "nixos";
            # Grow the root partition to fit the closure + headroom instead of
            # pinning a fixed size; keeps the image just large enough.
            Minimize = "guess";
          };
        };
      };
    };
  };

  sys = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = systemModules ++ [ repartModule ];
  };

  cfg = sys.config;
  pkgs = sys.pkgs;

  rawImage = cfg.system.build.image;
  rawFile = cfg.image.fileName;
in
# Convert the repart raw image to qcow2 host-side (qemu-img, no VM). Output dir holds
# satellite-host.qcow2 so the `mise run run` qcow2 lookup keeps working unchanged.
pkgs.runCommand "satellite-host-qcow2"
  {
    nativeBuildInputs = [ pkgs.qemu-utils ];
  }
  ''
    mkdir -p "$out"
    qemu-img convert -f raw -O qcow2 \
      "${rawImage}/${rawFile}" \
      "$out/satellite-host.qcow2"
  ''
