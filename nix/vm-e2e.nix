# The whole boot path in one test, each stage failing with its name.
#
# machine: OVMF, so the firmware leaves a BGRT logo and simpledrm shows the
# splash until virtio-gpu, held back until the real root as a native driver
# that loads late would be, takes over; systemd-boot into a specialisation
# whose root is LUKS, with a second LUKS disk under the same passphrase;
# lys-splash through its NixOS module; lys-login's greeter on lys-wm. The
# passphrase is typed at the splash once and both disks unlock; the splash
# that started in the initrd is the process that runs after the switch to
# the real root; nixpkgs' plymouth client talks to it; it hands its frame to
# the greeter with no blank in between; and a reboot shows the reboot splash
# until the VM resets.
#
# text: no display device, a LUKS root, and the passphrase typed at the
# splash's text fallback.
{
  pkgs,
  module,
  splash,
  lys-login,
  lys-wm,
}: let
  inherit (pkgs) lib;
  luks = disks: {
    imports = [module];
    boot.lys-splash = {
      enable = true;
      package = splash;
    };
    virtualisation = {
      emptyDiskImages = map (_: 512) disks;
      useBootLoader = true;
      useEFIBoot = true;
      mountHostNixStore = true;
      qemu.virtioKeyboard = false;
    };
    boot.loader.systemd-boot.enable = true;
    environment.systemPackages = [pkgs.cryptsetup];
    specialisation.luks.configuration = {
      boot.kernelParams = ["systemd.journald.forward_to_console=1"];
      boot.initrd.luks.devices = lib.mkVMOverride (lib.listToAttrs (lib.imap0 (i: d: lib.nameValuePair "crypt${toString i}" {device = d;}) disks));
      virtualisation.rootDevice = "/dev/mapper/crypt0";
      virtualisation.fileSystems = lib.optionalAttrs (builtins.length disks > 1) {
        "/data" = {
          device = "/dev/mapper/crypt1";
          fsType = "ext4";
        };
      };
    };
  };
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-e2e";
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {
      imports = [(luks ["/dev/vdb" "/dev/vdc"]) (import ./greeter.nix {inherit pkgs splash lys-login lys-wm;})];
      virtualisation.qemu.options = ["-vga none" "-device virtio-vga" "-action shutdown=pause"];
      boot.kernelParams = ["modprobe.blacklist=virtio_gpu"];
      systemd.services.native-gpu = {
        wantedBy = ["multi-user.target"];
        before = ["greeter-gate.service"];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = "${pkgs.kmod}/bin/modprobe virtio_gpu";
        };
      };
    };
    nodes.text = {
      imports = [(luks ["/dev/vdb"])];
      virtualisation.graphics = false;
      virtualisation.qemu.options = ["-vga none"];
    };
    testScript =
      ''
        plymouth = "${lib.getExe' pkgs.plymouth "plymouth"}"
        passphrase = "lys-e2e-passphrase"
      ''
      + builtins.readFile ./screens.py
      + builtins.readFile ./stages.py
      + builtins.readFile ./vm-e2e.py;
  }
