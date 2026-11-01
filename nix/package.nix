{
  perSystem = {
    lib,
    pkgs,
    ...
  }: let
    kernelCommandLineConditions = lib.concatMapStringsSep "\n" (c: "ConditionKernelCommandLine=${c}") [
      "!plymouth.enable=0"
      "!nosplash"
      "!splash=verbose"
      "|splash"
      "|rhgb"
    ];
    src = lib.fileset.toSource {
      root = ../.;
      fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
        lib.fileset.unions [
          ../flake.nix
          ../flake.lock
          ../nix
          ../docs
        ]
      );
    };
  in {
    packages.default = pkgs.buildGo127Module {
      pname = "lys-splash";
      version = "0-unstable-${lib.substring 0 8 (builtins.unsafeDiscardStringContext (baseNameOf src))}";

      inherit src;

      vendorHash = "sha256-lid0wTDXX6N4STsv2wZ2DoKGRPzLsyEkWM+1+2IlbKM=";

      env.CGO_ENABLED = 0;

      doCheck = true;

      outputs = ["out" "compat"];

      dontPatchShebangs = true;
      dontMoveSbin = true;

      postInstall = ''
        installUnits() {
          mkdir -p "$1"
          for unit in units/*; do
            substitute "$unit" "$1/$(basename "$unit")" \
              --subst-var-by lys_splash "$2" \
              --subst-var-by kernel_command_line_conditions ${lib.escapeShellArg kernelCommandLineConditions} \
              --subst-var-by systemd_tty_ask_password_agent "$3"
          done
        }
        installUnits $out/lib/systemd/system $out/bin/lys-splash ${lib.getExe' pkgs.systemd "systemd-tty-ask-password-agent"}

        install -Dm755 $out/bin/lys-splash $compat/bin/lys-splash
        ln -s lys-splash $compat/bin/plymouth
        mkdir -p $compat/sbin $compat/share
        ln -s ../bin/lys-splash $compat/sbin/plymouthd
        installUnits $compat/lib/systemd/system /usr/bin/lys-splash /usr/bin/systemd-tty-ask-password-agent
        cp -r initramfs-tools $compat/share/initramfs-tools

        if grep -rn '@[a-z_]*@' $out/lib $compat/lib; then
          echo "a unit names a placeholder that is not filled in" >&2
          exit 1
        fi
      '';

      meta = {
        description = "The lys desktop's boot splash";
        homepage = "https://github.com/stubbedev/lys-splash";
        license = lib.licenses.mit;
        mainProgram = "lys-splash";
        platforms = lib.platforms.unix;
      };
    };
  };
}
