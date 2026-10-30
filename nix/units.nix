# The names of lys-splash's systemd units, and each unit's WantedBy= targets
# read from its [Install] section, which NixOS does not apply on its own.
lib: let
  src = ../units;
  names = builtins.attrNames (builtins.readDir src);
  wantedBy = name: let
    lines = lib.splitString "\n" (builtins.readFile (src + "/${name}"));
  in
    lib.concatMap (l: lib.splitString " " (lib.removePrefix "WantedBy=" l)) (builtins.filter (lib.hasPrefix "WantedBy=") lines);
in {
  inherit names wantedBy;
}
