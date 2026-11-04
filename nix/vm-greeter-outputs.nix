{
  pkgs,
  splash,
  lys-login,
  lys-wm,
  wmPackage,
}: let
  grab = pkgs.callPackage ./drm-grab.nix {};
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-greeter-outputs";
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {
      imports = [(import ./greeter.nix {inherit pkgs splash lys-login lys-wm wmPackage;})];
      boot.kernelPackages = pkgs.linuxPackages_latest;
      boot.kernelModules = ["vkms"];
      boot.extraModprobeConfig = "options vkms create_default_dev=0";
      virtualisation.graphics = false;
      virtualisation.qemu.options = ["-vga none"];
      environment.systemPackages = [splash grab];
      systemd.services.vkms-outputs = {
        wantedBy = ["multi-user.target"];
        after = ["systemd-modules-load.service" "sys-kernel-config.mount"];
        requires = ["sys-kernel-config.mount"];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
        };
        script = ''
          set -euo pipefail
          d=/sys/kernel/config/vkms/splash
          mkdir "$d"
          for i in 1 2; do
            mkdir "$d/planes/plane$i" "$d/crtcs/crtc$i" "$d/encoders/encoder$i" "$d/connectors/connector$i"
            echo 1 > "$d/planes/plane$i/type"
            ln -s "$d/crtcs/crtc$i" "$d/planes/plane$i/possible_crtcs"
            ln -s "$d/crtcs/crtc$i" "$d/encoders/encoder$i/possible_crtcs"
            ln -s "$d/encoders/encoder$i" "$d/connectors/connector$i/possible_encoders"
          done
          echo 1 > "$d/enabled"
        '';
      };
    };
    testScript =
      builtins.readFile ./screens.py
      + builtins.readFile ./stages.py
      + builtins.readFile ./vm-greeter-outputs.py;
  }
