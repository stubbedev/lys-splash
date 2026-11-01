#!/usr/bin/env bash
# Installs the compat output into Ubuntu as docs/guide/install.md says, builds an
# initramfs-tools image there and checks that it holds the splash, the
# plymouth and plymouthd links and the scripts in initramfs-tools' order.
set -euo pipefail

image=${1:-ubuntu:24.04}
compat=$(nix build --no-link --print-out-paths "$(dirname "$0")/..#default.compat")

docker run --rm -v "$compat:/compat:ro" "$image" bash -euo pipefail -c '
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -qq -y --no-install-recommends initramfs-tools linux-image-virtual zstd >/dev/null
  if dpkg -s plymouth >/dev/null 2>&1; then
    echo "plymouth is installed alongside lys-splash" >&2
    exit 1
  fi
  cp -a --no-preserve=ownership /compat/. /usr/
  kernel=$(ls /lib/modules | head -n1)
  mkinitramfs -o /tmp/initrd.img "$kernel"
  lsinitramfs /tmp/initrd.img > /tmp/files
  for want in usr/bin/lys-splash usr/bin/plymouth usr/sbin/plymouthd \
    scripts/init-premount/lys-splash scripts/init-bottom/lys-splash scripts/panic/lys-splash; do
    grep -qx "$want" /tmp/files || { echo "the image has no $want" >&2; exit 1; }
  done
  root=$(mktemp -d)
  unmkinitramfs /tmp/initrd.img "$root"
  main=$root/main
  [ -d "$main" ] || main=$root
  for phase in init-premount init-bottom panic; do
    grep -q "/scripts/$phase/lys-splash" "$main/scripts/$phase/ORDER" || { echo "scripts/$phase/ORDER does not run lys-splash" >&2; exit 1; }
  done
  [ "$(readlink -f "$main/usr/sbin/plymouthd")" = "$(readlink -f "$main/usr/bin/lys-splash")" ] || { echo "plymouthd does not lead to lys-splash" >&2; exit 1; }
  rc=0
  chroot "$main" /usr/bin/plymouth --ping || rc=$?
  [ "$rc" = 3 ] || { echo "plymouth --ping in the image exited $rc, want 3 with no daemon" >&2; exit 1; }
  echo "the initramfs-tools image holds lys-splash"
'
