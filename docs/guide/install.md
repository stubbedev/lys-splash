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

The systemd units and initramfs-tools' script read it the same way, as one rule.
The splash runs when the command line asks for it and nothing turns it off:

| Parameter | What it does |
| --- | --- |
| `splash`, `splash=silent`, `rhgb` | Asks for the splash; one of them is needed |
| `nosplash` | Turns it off, wherever it stands on the command line |
| `splash=verbose` | Turns it off |
| `plymouth.enable=0` | Turns it off |

Turned off, no unit runs at any stage of the boot or the shutdown, and the kernel
and systemd keep the console, where systemd's console agent asks for passwords.
The order of the parameters does not matter, so a splash started in the initramfs
always has `lys-splash-quit.service` to end it. The units that show a splash do
not run in a container either.

Debian's Plymouth stops on `nosplash` in its units too. Where Plymouth falls back
to scrolling text without `splash`, lys-splash leaves the console to the kernel.

Without `splash` or `rhgb` on your bootloader's command line there is no splash, so
add one. The NixOS module adds `splash`.

## Check that it works

```sh
sudo lys-splash --ping
journalctl -b -u 'lys-splash*'
```

After the boot has finished `--ping` exits 3, because no splash is running any
more. The journal holds what a splash started by a unit said about its outputs,
its theme and its quit. A splash that initramfs-tools' script starts detaches
from its standard error, so its log goes nowhere.
