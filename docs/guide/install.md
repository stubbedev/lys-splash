# Install

## NixOS

Add the flake as an input and import its module:

```nix
{
  inputs.lys-splash.url = "github:stubbedev/lys-splash";

  outputs = {
    nixpkgs,
    lys-splash,
    ...
  }: {
    nixosConfigurations.machine = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ./configuration.nix
        lys-splash.nixosModules.default
        {boot.lys-splash.enable = true;}
      ];
    };
  };
}
```

| Option | Default | What it is |
| --- | --- | --- |
| `boot.lys-splash.enable` | `false` | Puts the binary, the theme and the units into the systemd initrd and the system, and adds `splash` to the kernel command line |
| `boot.lys-splash.package` | This flake's package | The package to run |
| `boot.lys-splash.theme` | `{}` | The fields of [`theme.yaml`](/guide/configuration) |

It cannot be combined with `boot.plymouth.enable`; an assertion names both
options.

The module needs NixOS's systemd initrd, which is the only one on current
nixos-unstable. On a release that still has the scripted one, also set
`boot.initrd.systemd.enable = true`.

## Ubuntu and Debian

The package's `compat` output is a tree to copy under `/usr`. Building it needs
Nix; the machine it is built on can be another one.

```sh
nix build github:stubbedev/lys-splash#default.compat
sudo apt-get remove plymouth
sudo cp -a --no-preserve=ownership result-compat/. /usr/
sudo systemctl enable lys-splash-start.service lys-splash-quit.service \
  lys-splash-quit-wait.service lys-splash-read-write.service \
  lys-splash-ask-password.path lys-splash-reboot.service \
  lys-splash-poweroff.service lys-splash-halt.service lys-splash-kexec.service
sudo update-initramfs -u
```

The tree holds:

| Path | What it is |
| --- | --- |
| `bin/lys-splash` | The binary |
| `bin/plymouth`, `sbin/plymouthd` | Links to it, which pick the [role](/guide/command-line) it runs in |
| `lib/systemd/system/` | The [units](/guide/how-it-works#the-units), with `/usr` paths |
| `share/initramfs-tools/` | The hook, which copies the binary, the links and the theme into the image, and the `init-premount`, `init-bottom` and `panic` scripts |

cryptsetup's initramfs scripts call `plymouth`, so they ask through the splash
unchanged.

CI builds such an image in Ubuntu 24.04 and checks that it holds the splash,
the links and the scripts in initramfs-tools' order. It does not boot it; whole
boots are tested on NixOS.

## The kernel command line

| Parameter | initramfs-tools' script | systemd units |
| --- | --- | --- |
| `splash` | Needed: starts the splash in the initrd | Ignored |
| `nosplash` | Does not start it, unless a `splash` follows | Ignored |
| `plymouth.enable=0` | Does not start it, unless a `splash` follows | Do not run |

So `plymouth.enable=0` at the end of the command line turns the splash off
everywhere, and it is the only one of the three that does. `nosplash` alone only
keeps it out of the initramfs: `lys-splash-start.service` still starts it in the
real root. The units that show a splash do not run in a
container either.

If your bootloader's command line has no `splash`, add it, or the splash on
Ubuntu and Debian only starts once systemd runs in the real root, too late for
disk passwords in the initramfs.

## Check that it works

```sh
sudo lys-splash --ping
journalctl -b -u 'lys-splash*'
```

After the boot has finished `--ping` exits 3, because no splash is running any
more. The journal holds what a splash started by a unit said about its outputs,
its theme and its quit. A splash that initramfs-tools' script starts detaches
from its standard error, so its log goes nowhere.
