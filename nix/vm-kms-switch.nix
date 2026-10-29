{
  pkgs,
  splash,
}:
pkgs.testers.runNixOSTest {
  name = "lys-splash-kms-switch";
  extraPythonPackages = p: [p.pillow];
  nodes.machine = {
    virtualisation.useEFIBoot = true;
    virtualisation.qemu.options = ["-vga none" "-device virtio-vga"];
    boot.kernelParams = ["modprobe.blacklist=virtio_gpu" "fbcon=map:1"];
    systemd.services."getty@tty1".enable = false;
    systemd.services."autovt@tty1".enable = false;
    environment.systemPackages = [splash];
  };
  testScript = builtins.readFile ./screens.py + builtins.readFile ./vm-kms-switch.py;
}
