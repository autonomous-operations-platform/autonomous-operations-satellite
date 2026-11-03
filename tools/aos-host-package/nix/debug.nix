# debug.nix — break-glass console access for the DEBUG image variant only.
#
# The hardened `satellite-host` / `image` deliberately provide no interactive login.
# `mise run run` would then boot to an unusable prompt. This module adds the minimum
# needed to get a shell when iterating locally, and is imported ONLY by
# `satellite-host-debug` / `image-debug` in flake.nix — never by the published image.
#
# Access path: QEMU `-nographic` wires the guest's serial console to your terminal,
# so you land at a `login:` and sign in as root with the dev password below.
#
# FIPS and the hardened service are inherited from the base config, so this debug
# boot still exercises the real runtime — it only ADDS a way in.

{ lib, pkgs, ... }:

{
  # Serial console on the aarch64 `virt` machine QEMU uses. Setting this makes NixOS
  # start a getty on ttyAMA0, which `-nographic` connects to your terminal.
  boot.kernelParams = [ "console=ttyAMA0,115200" "console=tty0" ];

  # Dev-only break-glass credential. initialPassword is plaintext and world-visible in
  # the store — acceptable for a throwaway local VM, NEVER for the published artifact.
  # This is why the module lives outside the hardened config.
  users.users.root.initialPassword = lib.mkForce "nixos";

  # Make the serial getty prompt promptly instead of waiting on multi-user niceties.
  systemd.services."serial-getty@ttyAMA0".enable = true;

  # A MySQL-protocol client at the console so you can inspect Doris directly from the
  # debug shell (`mysql -h127.0.0.1 -P9030 -uroot`, SHOW BACKENDS, etc.). Debug-only:
  # the hardened image deliberately ships no SQL client.
  environment.systemPackages = [ pkgs.mariadb.client ];
}
