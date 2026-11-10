# fips.nix — ADR-024 FIPS 140-2/3 host wiring (opt-in kernel rebuild).
#
# Two separable pieces, because one without the other is a trap:
#
#   1. boot.kernelParams = [ "fips=1" ] — asks the kernel to enter FIPS mode at boot.
#      This is INERT unless the kernel was built with CONFIG_CRYPTO_FIPS. The stock
#      nixpkgs kernel is NOT, so on its own the param is a silent no-op:
#      /proc/sys/crypto/fips_enabled never appears (confirmed by the VM boot test).
#
#   2. A kernel rebuilt with CONFIG_CRYPTO_FIPS (+ its Linux-6.12 dependency chain).
#      This is what actually makes fips=1 take effect — but it triggers a full kernel
#      COMPILE, which is many minutes natively and painfully slow under QEMU/TCG.
#
# Because (2) is expensive and only meaningful on the real target, the kernel rebuild
# is OPT-IN via `services.aosSatellite.fips.rebuildKernel`, DEFAULT OFF:
#   - off (default): fips=1 is set, but the host boots the stock kernel. FIPS is NOT
#     active at runtime. Suitable for dev/CI image builds where you don't want a kernel
#     compile. This is the current, honestly-unproven state.
#   - on: rebuilds the kernel with CONFIG_CRYPTO_FIPS so FIPS actually engages. Turn this
#     on for the real (preferably x86_64 / KVM) build of the appliance, and verify with
#     the VM boot test (checks.<system>.boot-test) or on the booted target.
#
# Dependency chain for CRYPTO_FIPS on Linux 6.12 (all must hold or the option is
# silently "unused", which is fatal in nixpkgs' config checker):
#   depends on (CRYPTO_ANSI_CPRNG || CRYPTO_DRBG)   -> enable CRYPTO_DRBG
#   depends on !CRYPTO_MANAGER_DISABLE_TESTS        -> keep self-tests (mkForce: default y)
#   depends on (MODULE_SIG || !MODULES)             -> MODULE_SIG (mkForce: nixpkgs sets n)

{ config, lib, pkgs, ... }:

let
  cfg = config.services.aosSatellite.fips;
in
{
  options.services.aosSatellite.fips = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Pass `fips=1` so the kernel enters FIPS mode at boot. Inert unless the kernel
        was built with CONFIG_CRYPTO_FIPS (see `rebuildKernel`).
      '';
    };

    rebuildKernel = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Rebuild the kernel with CONFIG_CRYPTO_FIPS so `fips=1` actually takes effect.
        OFF by default because it triggers a full kernel compile (slow, and extremely
        slow under QEMU/TCG). Turn ON for the real appliance build and verify FIPS is
        active (`/proc/sys/crypto/fips_enabled == 1`), e.g. via the VM boot test.

        When OFF, `fips=1` is still set but FIPS is NOT active at runtime.
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      boot.kernelParams = [ "fips=1" ];
    })

    (lib.mkIf cfg.rebuildKernel {
      boot.kernelPatches = [{
        name = "enable-crypto-fips";
        patch = null;
        structuredExtraConfig = with lib.kernel; {
          CRYPTO_FIPS = yes;
          CRYPTO_DRBG = yes;                                 # PRNG dependency
          CRYPTO_MANAGER_DISABLE_TESTS = lib.mkForce no;     # self-tests required
          MODULE_SIG = lib.mkForce yes;                      # MODULES is on
          CRYPTO_USER_API_HASH = yes;
          CRYPTO_USER_API_SKCIPHER = yes;
        };
      }];
    })
  ];
}
