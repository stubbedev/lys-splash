# A NixOS boot through lys-splash's units, on two VMs at once.
#
# machine, on virtio-gpu with the German de-latin1 console keymap: the units pass
# systemd-analyze verify and run in plymouth's order, which the journal
# shows; the splash that started in the initrd is the same process after the
# switch to the real root, chrooted into it; plymouth --ping through the link
# answers; a password typed on the German layout arrives with its
# characters; Esc shows the boot messages and Esc again the splash; the
# greeter, a display manager following the README's contract, keeps
# lys-splash-quit and the getty on tty1 from running, and its hand-over
# (deactivate, its compositor opens the card, quit --retain-splash) keeps
# the frame on screen and records the boot's duration; and a reboot shows
# the reboot splash until the VM resets, which QEMU turns into a pause so
# the last frame can be read. The greeter holds its hand-over until the test
# has used the splash.
#
# text, without any display device: the text fallback answers a password,
# and lys-splash-quit gives the console, here the serial port, its captured
# messages back. Nothing quits its splash on its own, so the test decides
# when.
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
  quitByTest = {
    systemd.services.lys-splash-quit.wantedBy = lib.mkForce [];
    systemd.services."getty@tty1".enable = false;
    systemd.services."autovt@tty1".enable = false;
  };
  greeter = {
    systemd.services.greeter = {
      wantedBy = ["multi-user.target"];
      after = ["lys-splash-start.service" "getty@tty1.service" "lys-splash-quit.service" "systemd-user-sessions.service"];
      conflicts = ["lys-splash-quit.service" "getty@tty1.service"];
      path = [pkgs.coreutils pkgs.systemd];
      serviceConfig = {
        Type = "notify";
        NotifyAccess = "all";
        TimeoutStartSec = "infinity";
        ExecStartPre = [
          "${pkgs.runtimeShell} -c 'until test -e /run/greeter-go; do sleep 0.1; done'"
          "-${lib.getExe splash} deactivate"
        ];
        ExecStart = "${pkgs.runtimeShell} -c 'exec 3<>/dev/dri/card0; systemd-notify --ready; exec sleep infinity'";
        ExecStartPost = "-${lib.getExe splash} quit --retain-splash";
      };
    };
  };
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-boot";
    enableOCR = true;
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {
      imports = [splashUnits greeter];
      virtualisation.qemu.options = ["-vga none" "-device virtio-gpu-pci" "-action shutdown=pause"];
      boot.initrd.kernelModules = ["virtio_gpu"];
      virtualisation.qemu.virtioKeyboard = false;
      console.keyMap = "de-latin1";
      systemd.services.lys-splash-reboot.serviceConfig.StandardError = "file:/dev/kmsg";
    };
    nodes.text = {
      imports = [splashUnits quitByTest];
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
