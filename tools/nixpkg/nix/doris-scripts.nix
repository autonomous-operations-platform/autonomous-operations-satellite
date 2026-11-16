# doris-scripts.nix — the Doris runtime logic as wrapped, standalone shell scripts.
#
# SINGLE SOURCE OF TRUTH. The seed / derive / set-priority-networks / register logic used
# to live inlined as Nix string fragments inside doris-module.nix, so it only ran on
# NixOS. It now lives as real .sh files in ../scripts/, wrapped here with
# pkgs.writeShellApplication so that:
#   - the right tools are baked onto each script's PATH (no reliance on the host distro
#     putting getopt/sed/awk/ip/… on PATH — the original `dorisScriptPath` problem), and
#   - shellcheck runs at build time, and
#   - BOTH consumers — the NixOS module (doris-module.nix) and the portable systemd
#     installer (doris-systemd.nix) — call the exact same wrapped scripts.
#
# The scripts take their formerly-interpolated values (package path, priority_networks,
# mysql client) as arguments / environment variables instead, so they are distro-agnostic
# and independently runnable/testable. See each script's header for its interface.
#
# Returns an attrset of wrapped derivations. Each `${scripts.<name>}` is a /nix/store path
# whose /bin/<name> is the executable; `scripts.<name>.pname` is the bare command name.

{ pkgs }:

let
  inherit (pkgs) writeShellApplication;

  # The tools the upstream start_*/stop_* scripts AND our extracted logic shell out to.
  # Determined from the scripts, not guessed:
  #   util-linux → getopt, swapon      gnused → sed       gnugrep → grep
  #   gawk       → awk                 procps → kill, free, sysctl
  #   iproute2   → ip (deriveNetAddr/setPriorityNetworks)
  #   coreutils  → uname, date, tr, nohup, cat, wc, chmod, mkdir, env, pwd, echo, seq, …
  baseInputs = [
    pkgs.bash
    pkgs.coreutils
    pkgs.util-linux
    pkgs.gnused
    pkgs.gnugrep
    pkgs.gawk
    pkgs.procps
    pkgs.iproute2
  ];

  seed-home = writeShellApplication {
    name = "doris-seed-home.sh";
    runtimeInputs = baseInputs;
    text = builtins.readFile ../scripts/doris-seed-home.sh;
  };

  derive-netaddr = writeShellApplication {
    name = "doris-derive-netaddr.sh";
    runtimeInputs = baseInputs;
    text = builtins.readFile ../scripts/doris-derive-netaddr.sh;
  };

  set-priority-networks = writeShellApplication {
    name = "doris-set-priority-networks.sh";
    runtimeInputs = baseInputs;
    text = builtins.readFile ../scripts/doris-set-priority-networks.sh;
  };

  # The register script invokes `doris-derive-netaddr.sh` by name and a `mysql` client,
  # so both must be on ITS runtime PATH. mariadb.client ships `mysql` (client only).
  register-be = writeShellApplication {
    name = "doris-register-be.sh";
    runtimeInputs = baseInputs ++ [ derive-netaddr pkgs.mariadb.client ];
    text = builtins.readFile ../scripts/doris-register-be.sh;
  };
in
{
  inherit seed-home derive-netaddr set-priority-networks register-be;
}
