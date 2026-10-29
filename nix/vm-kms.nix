{
  pkgs,
  splash,
  kms,
}: let
  grab = pkgs.runCommandCC "drm-grab" {
    nativeBuildInputs = [pkgs.pkg-config];
    buildInputs = [pkgs.libdrm];
  } "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/drm-grab ${./drm-grab.c} $(pkg-config --cflags --libs libdrm)";
in
  pkgs.testers.runNixOSTest {
    name = "lys-splash-kms";
    extraPythonPackages = p: [p.pillow];
    nodes.machine = {
      boot.kernelPackages = pkgs.linuxPackages_latest;
      boot.kernelModules = ["vkms"];
      boot.extraModprobeConfig = "options vkms create_default_dev=0";
      boot.kernelParams = ["video=Virtual-2:panel_orientation=right_side_up"];
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
      ''
        kms_test = "${kms}/bin/kms.test"
      ''
      + builtins.readFile ./screens.py
      + builtins.readFile ./vm-kms.py;
  }
