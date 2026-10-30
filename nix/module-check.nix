# The NixOS module evaluates and puts the splash into the initrd: the
# binary, the systemd password agent, the units and the theme are in the
# initrd's image, the system has the units and splash on the kernel command
# line, and its assertion fires next to boot.plymouth, naming both options.
{
  pkgs,
  module,
}: let
  inherit (pkgs) lib;
  eval = extra:
    (import (pkgs.path + "/nixos/lib/eval-config.nix") {
      inherit (pkgs.stdenv.hostPlatform) system;
      modules = [
        module
        {
          boot.lys-splash = {
            enable = true;
            theme.throbber = "dots";
          };
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "/dev/vda";
            fsType = "ext4";
          };
          system.stateVersion = lib.trivial.release;
        }
        extra
      ];
    }).config;
  failed = config: map (a: a.message) (builtins.filter (a: !a.assertion) config.assertions);
  enabled = eval {};
  splash = enabled.boot.lys-splash.package;
  withPlymouth = failed (eval {boot.plymouth.enable = true;});
  names = needles: messages: builtins.any (m: builtins.all (n: lib.hasInfix n m) needles) messages;
  expect = what: ok: lib.optionalString (!ok) "echo ${lib.escapeShellArg what} >&2; exit 1\n";
in
  pkgs.runCommand "lys-splash-module-check" {nativeBuildInputs = [pkgs.cpio pkgs.zstd];} (
    expect "the module's assertions fail on their own: ${toString (failed enabled)}" (failed enabled == [])
    + expect "no assertion names boot.lys-splash.enable and boot.plymouth.enable: ${toString withPlymouth}" (names ["boot.lys-splash.enable" "boot.plymouth.enable"] withPlymouth)
    + expect "splash is not on the kernel command line" (builtins.elem "splash" enabled.boot.kernelParams)
    + expect "the system does not start lys-splash-quit" (builtins.elem "multi-user.target" enabled.systemd.services.lys-splash-quit.wantedBy)
    + ''
      zstd -dc ${enabled.system.build.initialRamdisk}/${enabled.system.boot.loader.initrdFile} | cpio -it --quiet > files
      for want in \
        ${lib.removePrefix "/" (lib.getExe splash)} \
        ${lib.removePrefix "/" (lib.getExe' enabled.systemd.package "systemd-tty-ask-password-agent")} \
        'nix/store/[^/]*-initrd-units/lys-splash-start.service' \
        'nix/store/[^/]*-initrd-units/lys-splash-switch-root.service' \
        'nix/store/[^/]*-initrd-units/lys-splash-ask-password.path' \
        'nix/store/[^/]*-initrd-units/sysinit.target.wants/lys-splash-start.service' \
        'nix/store/[^/]*-initrd-units/initrd-switch-root.target.wants/lys-splash-switch-root.service' \
        'nix/store/[^/]*-initrd-units/sysinit.target.wants/lys-splash-ask-password.path' \
        etc/lys-splash/theme.yaml; do
        grep -qx "$want" files || { echo "the initrd has no $want" >&2; exit 1; }
      done
      grep -q 'throbber: dots' ${enabled.environment.etc."lys-splash/theme.yaml".source}
      touch $out
    ''
  )
