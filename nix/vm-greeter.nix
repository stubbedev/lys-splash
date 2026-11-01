# lys-splash hands the screen to lys-login's greeter on lys-wm without a
# blank: the splash boots through the NixOS module on virtio-gpu, and
# lys-login, the display manager, follows the contract in
# docs/guide/how-it-works.md. The
# greeter's start waits on the test, which then dumps the screen as fast as
# the VM keeps up from the splash's last frame until the greeter shows, and
# reads the kernel's DRM log from that point on.
#
# Every dump shows either the splash, pixel for pixel, or the greeter's
# picture; none is black, the console or lys-wm's empty background. The quit
# came from lys-login with retain, lys-splash-quit and the getty on tty1
# never ran, and lys-wm's first commit after the splash dropped master is a
# nonblocking flip with no modeset, so the kernel's fbdev emulation never
# restored its console in between.
{
  pkgs,
  module,
  splash,
  lys-login,
  lys-wm,
  wmPackage,
}:
pkgs.testers.runNixOSTest {
  name = "lys-splash-greeter";
  extraPythonPackages = p: [p.pillow];
  nodes.machine = {
    imports = [module (import ./greeter.nix {inherit pkgs splash lys-login lys-wm wmPackage;})];
    boot.lys-splash = {
      enable = true;
      package = splash;
    };
    boot.initrd.kernelModules = ["virtio_gpu"];
    virtualisation.qemu.options = ["-vga none" "-device virtio-gpu-pci"];
  };
  testScript =
    builtins.readFile ./screens.py
    + builtins.readFile ./stages.py
    + builtins.readFile ./vm-greeter.py;
}
