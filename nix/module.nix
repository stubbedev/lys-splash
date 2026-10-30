{self, ...}: {
  flake.nixosModules.default = {
    config,
    lib,
    pkgs,
    ...
  }: let
    cfg = config.boot.lys-splash;
    units = import ./units.nix lib;
    enable = kind: only:
      lib.listToAttrs (map (name: lib.nameValuePair (lib.removeSuffix ".${kind}" name) {wantedBy = units.wantedBy name;})
        (builtins.filter (name: lib.hasSuffix ".${kind}" name && (only == null || builtins.elem name only)) units.names));
    themeFile = (pkgs.formats.yaml {}).generate "lys-splash-theme.yaml" cfg.theme;
    agent = systemd: lib.getExe' systemd "systemd-tty-ask-password-agent";
    askPassword = systemd: {
      lys-splash-ask-password.serviceConfig.ExecStart = ["" "${agent systemd} --watch --plymouth"];
    };
  in {
    options.boot.lys-splash = {
      enable = lib.mkEnableOption "lys-splash, the lys desktop's boot splash, in place of plymouth";
      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        defaultText = lib.literalExpression "lys-splash.packages.\${pkgs.stdenv.hostPlatform.system}.default";
        description = "The lys-splash package to run.";
      };
      theme = lib.mkOption {
        inherit (pkgs.formats.yaml {}) type;
        default = {};
        example = {
          accent = "#7aa2f7";
          throbber = "dots";
        };
        description = "theme.yaml's fields; those left out keep the embedded theme's.";
      };
    };

    config = lib.mkIf cfg.enable {
      assertions = [
        {
          assertion = !config.boot.plymouth.enable;
          message = "boot.lys-splash.enable and boot.plymouth.enable cannot both be set: lys-splash takes plymouth's place.";
        }
      ];

      boot.kernelParams = ["splash"];

      boot.initrd.systemd = {
        packages = [cfg.package];
        storePaths = [
          (lib.getExe cfg.package)
          (agent config.boot.initrd.systemd.package)
        ];
        services = enable "service" ["lys-splash-start.service" "lys-splash-switch-root.service"] // askPassword config.boot.initrd.systemd.package;
        paths = enable "path" null;
        contents = lib.mkIf (cfg.theme != {}) {"/etc/lys-splash/theme.yaml".source = themeFile;};
      };

      systemd.packages = [cfg.package];
      systemd.services = lib.recursiveUpdate (enable "service" null) (askPassword config.systemd.package);
      systemd.paths = enable "path" null;
      environment.systemPackages = [cfg.package];
      environment.etc."lys-splash/theme.yaml" = lib.mkIf (cfg.theme != {}) {source = themeFile;};
    };
  };
}
