# lys-login as the display manager, showing its greeter in a lys-wm kiosk
# and taking the screen from lys-splash. greeter-gate holds lys-login until
# the test creates /run/greeter-go, then marks the kernel log, so a test can
# use the splash first and read the DRM log from the hand-over on.
{
  pkgs,
  splash,
  lys-login,
  lys-wm,
}: {config, ...}: {
  imports = [lys-login.nixosModules.default lys-wm.nixosModules.default];
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
      echo 'greeter-gate: the greeter may start' > /dev/kmsg
    '';
  };
  virtualisation.cores = 4;
  virtualisation.memorySize = 2048;
  environment.sessionVariables.LIBGL_ALWAYS_SOFTWARE = "1";
  hardware.graphics.enable = true;
  hardware.graphics.extraPackages = [pkgs.mesa];
  fonts.packages = [pkgs.inter];
}
