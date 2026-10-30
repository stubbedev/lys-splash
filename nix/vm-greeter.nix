# lys-splash hands the screen to lys-login's greeter on lys-wm without a
# blank: the splash boots through the NixOS module on virtio-gpu, and
# lys-login, the display manager, follows the README's contract. The
# greeter's start waits on the test, which then dumps the screen as fast as
# QEMU allows from the splash's last frame until the greeter shows, and
# reads the kernel's DRM log from that point on.
#
# Every dump shows either the splash, pixel for pixel, or the greeter's
# picture; none is black or the console. The quit came from lys-login with
# retain, lys-splash-quit and the getty on tty1 never ran, and lys-wm's
# first commit after the splash dropped master is a nonblocking flip with no
# modeset, so the kernel's fbdev emulation never restored its console in
# between.
{
  pkgs,
  module,
  splash,
  lys-login,
  lys-wm,
}: let
  inherit (pkgs) lib;
  marker = "vm-greeter: the greeter may start";
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-greeter";
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {config, ...}: {
      imports = [module lys-login.nixosModules.default lys-wm.nixosModules.default];
      boot.lys-splash = {
        enable = true;
        package = splash;
      };
      boot.initrd.kernelModules = ["virtio_gpu"];
      boot.kernelParams = ["drm.debug=0x10" "log_buf_len=64M"];
      programs.lys-wm.enable = true;
      programs.lys-login.greeter = {
        enable = true;
        displayManager = "lys-login";
        compositorPackage = config.programs.lys-wm.package;
        splashPackage = splash;
        session = {
          dirs = [];
          x11Dirs = [];
          command = "${pkgs.coreutils}/bin/sleep infinity";
        };
      };
      systemd.services.greeter-gate = {
        wantedBy = ["lys-login.service"];
        before = ["lys-login.service"];
        serviceConfig = {
          Type = "oneshot";
          TimeoutStartSec = "infinity";
        };
        script = ''
          until test -e /run/greeter-go; do sleep 0.1; done
          echo '${marker}' > /dev/kmsg
        '';
      };
      virtualisation.cores = 4;
      virtualisation.memorySize = 2048;
      virtualisation.qemu.options = ["-vga none" "-device virtio-gpu-pci"];
      environment.sessionVariables.LIBGL_ALWAYS_SOFTWARE = "1";
      hardware.graphics.enable = true;
      hardware.graphics.extraPackages = [pkgs.mesa];
      fonts.packages = [pkgs.inter];
    };
    testScript =
      ''
        marker = "${marker}"
      ''
      + builtins.readFile ./screens.py
      + builtins.readFile ./vm-greeter.py;
  }
