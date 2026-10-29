# Every unit conflicts with the plymouth unit it stands in for and stays off
# under plymouth.enable=0. systemd-analyze verify needs a running system's
# /run, so vm-boot runs it.
{
  pkgs,
  units,
}:
pkgs.runCommand "lys-splash-units-check" {} ''
  for unit in ${units.package}/lib/systemd/system/*; do
    name=$(basename "$unit")
    case $name in
      lys-splash-ask-password.*) counterpart=systemd-ask-password-plymouth.''${name##*.} ;;
      *) counterpart=plymouth-''${name#lys-splash-} ;;
    esac
    grep -Eq "^Conflicts=(.* )?$counterpart( |$)" "$unit" || {
      echo "$name does not conflict with $counterpart" >&2
      exit 1
    }
    grep -qx 'ConditionKernelCommandLine=!plymouth.enable=0' "$unit" || {
      echo "$name runs with plymouth.enable=0" >&2
      exit 1
    }
  done
  touch $out
''
