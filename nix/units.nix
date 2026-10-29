# lys-splash's systemd units with the binaries' store paths filled in, and
# each unit's WantedBy= targets read from its [Install] section, which
# NixOS does not apply on its own.
{
  lib,
  runCommand,
  splash,
  systemd,
}: let
  src = ../units;
  names = builtins.attrNames (builtins.readDir src);
  wantedBy = name: let
    lines = lib.splitString "\n" (builtins.readFile (src + "/${name}"));
  in
    lib.concatMap (l: lib.splitString " " (lib.removePrefix "WantedBy=" l)) (builtins.filter (lib.hasPrefix "WantedBy=") lines);
in {
  inherit names wantedBy;
  package = runCommand "lys-splash-units" {} ''
    mkdir -p $out/lib/systemd/system
    for unit in ${src}/*; do
      substitute "$unit" "$out/lib/systemd/system/$(basename "$unit")" \
        --subst-var-by lys_splash ${lib.getExe splash} \
        --subst-var-by systemd_tty_ask_password_agent ${lib.getExe' systemd "systemd-tty-ask-password-agent"}
    done
    if grep -rn '@[a-z_]*@' $out; then
      echo "a unit names a placeholder that is not filled in" >&2
      exit 1
    fi
  '';
}
