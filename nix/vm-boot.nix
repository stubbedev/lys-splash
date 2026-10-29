# A NixOS boot through lys-splash's units, on two VMs at once.
#
# machine, on virtio-gpu with the German de-latin1 console keymap: the units pass
# systemd-analyze verify and run in plymouth's order, which the journal
# shows; the splash that started in the initrd is the same process after the
# switch to the real root, chrooted into it; plymouth --ping through the link
# answers; a password typed on the German layout arrives with its
# characters; Esc shows the boot messages and Esc again the splash; the
# display-manager hand-over (deactivate, the next master opens the card, quit
# --retain-splash) keeps the frame on screen and records the boot's
# duration; and a reboot shows the reboot splash until the VM resets, which
# QEMU turns into a pause so the last frame can be read.
#
# text, without any display device: the text fallback answers a password,
# and lys-splash-quit gives the console, here the serial port, its captured
# messages back.
#
# Both boot as under a display manager: nothing quits the splash on its own,
# so the test decides when, and no getty takes tty1 back to text.
{
  pkgs,
  splash,
  units,
}: let
  inherit (pkgs) lib;
  plymouthLink = pkgs.runCommand "lys-splash-plymouth-link" {} ''
    mkdir -p $out/bin
    ln -s ${lib.getExe splash} $out/bin/plymouth
  '';
  enable = kind: only:
    lib.listToAttrs (map (name: lib.nameValuePair (lib.removeSuffix ".${kind}" name) {wantedBy = units.wantedBy name;})
      (builtins.filter (name: lib.hasSuffix ".${kind}" name && (only == null || builtins.elem name only)) units.names));
  splashUnits = {config, ...}: {
    boot.initrd.systemd = {
      packages = [units.package];
      storePaths = [
        (lib.getExe splash)
        (lib.getExe' config.systemd.package "systemd-tty-ask-password-agent")
      ];
      services = enable "service" ["lys-splash-start.service" "lys-splash-switch-root.service"];
      paths = enable "path" null;
    };
    systemd.packages = [units.package];
    systemd.services = enable "service" null;
    systemd.paths = enable "path" null;
    environment.systemPackages = [splash plymouthLink];
  };
  underDisplayManager = {
    systemd.services.lys-splash-quit.wantedBy = lib.mkForce [];
    systemd.services."getty@tty1".enable = false;
    systemd.services."autovt@tty1".enable = false;
  };
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-boot";
    enableOCR = true;
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {
      imports = [splashUnits underDisplayManager];
      virtualisation.qemu.options = ["-vga none" "-device virtio-gpu-pci" "-action shutdown=pause"];
      boot.initrd.kernelModules = ["virtio_gpu"];
      virtualisation.qemu.virtioKeyboard = false;
      console.keyMap = "de-latin1";
      systemd.services.lys-splash-reboot.serviceConfig.StandardError = "file:/dev/kmsg";
    };
    nodes.text = {
      imports = [splashUnits underDisplayManager];
      virtualisation.graphics = false;
      virtualisation.qemu.options = ["-vga none"];
    };
    testScript =
      ''
        units = "${units.package}/lib/systemd/system"
      ''
      + builtins.readFile ./screens.py
      + builtins.readFile ./vm-boot.py;
  }
