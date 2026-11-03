# boot-test.nix — NixOS VM test: boot the satellite host and assert the ADR-024 runtime
# invariants (FIPS active, hardened satellite service running).
#
# This boots the REAL system config under QEMU (TCG in-container; no host QEMU needed)
# and runs assertions in the Python driver. It is the reproducible, in-Nix answer to
# "does the image actually come up FIPS-enabled with the service running?" — replacing
# a manual `cat /proc/sys/crypto/fips_enabled` on a hand-booted qcow.

{ pkgs, satellite }:

pkgs.testers.runNixOSTest {
  name = "aos-satellite-host-boot";

  node.specialArgs = { inherit satellite; };

  nodes.machine = { config, pkgs, satellite, ... }: {
    imports = [ ./module.nix ./fips.nix ];

    services.aosSatellite = {
      enable = true;
      package = satellite;
    };

    # FIPS (ADR-024) wired by ./fips.nix. rebuildKernel = true so the test proves FIPS
    # is actually active — this is the one place we pay the kernel compile on purpose.
    services.aosSatellite.fips.rebuildKernel = true;

    system.stateVersion = "25.05";
  };

  testScript = ''
    machine.start()
    machine.wait_for_unit("multi-user.target")

    # ADR-024: the host boots with the kernel crypto subsystem in FIPS mode.
    fips = machine.succeed("cat /proc/sys/crypto/fips_enabled").strip()
    assert fips == "1", f"expected FIPS enabled (1), got {fips!r}"

    # The hardened satellite service came up and is running the verified binary.
    machine.wait_for_unit("aos-satellite.service")
    machine.succeed("systemctl is-active aos-satellite.service")

    # Hardening actually applied (spot-check a couple of the systemd knobs).
    props = machine.succeed(
        "systemctl show aos-satellite.service "
        "-p NoNewPrivileges -p User -p CapabilityBoundingSet"
    )
    assert "NoNewPrivileges=yes" in props, props
    assert "User=aos-satellite" in props, props

    # The service is executing the verified satellite from the Nix store.
    machine.succeed(
        "systemctl show aos-satellite.service -p ExecStart "
        "| grep -q /bin/hello-world-satellite"
    )
  '';
}
