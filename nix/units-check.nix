# Every unit conflicts with the plymouth unit it stands in for, and every unit
# and the initramfs-tools script read the kernel command line the same way:
# each row of the table below is put to systemd's own condition code and to the
# script. systemd-analyze verify needs a running system's /run, so vm-boot runs
# it.
{
  pkgs,
  splash,
}:
pkgs.runCommand "lys-splash-units-check" {nativeBuildInputs = [pkgs.systemd];} ''
  set -euo pipefail
  export HOME=$PWD XDG_RUNTIME_DIR=$PWD

  expect() {
    want=$1
    cmdline=$2

    for unit in ${splash}/lib/systemd/system/*; do
      mapfile -t conditions < <(grep '^ConditionKernelCommandLine=' "$unit")
      SYSTEMD_PROC_CMDLINE="$cmdline" systemd-analyze --user condition "''${conditions[@]}" > analyze.log 2>&1 || true
      case $(tail -n 1 analyze.log) in
        "Conditions succeeded.") got=on ;;
        "Conditions failed.") got=off ;;
        *)
          echo "systemd-analyze did not evaluate $(basename "$unit"):" >&2
          cat analyze.log >&2
          exit 1
          ;;
      esac
      [ "$got" = "$want" ] || {
        echo "$(basename "$unit") is $got with '$cmdline', want $want:" >&2
        cat analyze.log >&2
        exit 1
      }
    done

    printf '%s\n' "$cmdline" > cmdline
    rm -f started
    sed -e "s|/proc/cmdline|$PWD/cmdline|" \
      -e "s|/run/plymouth|$PWD/run|" \
      -e "s|/usr/sbin/plymouthd|touch $PWD/started; true|" \
      -e "s|/usr/bin/plymouth|true|" \
      ${splash.compat}/share/initramfs-tools/scripts/init-premount/lys-splash > premount
    sh premount
    if [ -e started ]; then got=on; else got=off; fi
    [ "$got" = "$want" ] || {
      echo "initramfs-tools' script is $got with '$cmdline', want $want" >&2
      exit 1
    }
  }

  expect on 'quiet splash'
  expect on 'splash=silent'
  expect on 'rhgb quiet'
  expect off 'quiet'
  expect off 'splash=verbose'
  expect off 'quiet nosplash'
  expect off 'nosplash splash'
  expect off 'splash nosplash'
  expect off 'splash plymouth.enable=0'
  expect off 'plymouth.enable=0 splash'
  expect off 'nosplashy'
  expect off 'splashy'

  for unit in ${splash}/lib/systemd/system/* ${splash.compat}/lib/systemd/system/*; do
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
