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

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `boot.lys-splash.enable` | bool | `false` | Installs the binary, theme and units into the systemd initrd and the system, and adds `splash` to the kernel command line |
| `boot.lys-splash.package` | package | this flake's package | The package to run |
| `boot.lys-splash.theme` | YAML attrs | `{}` | The fields of [`theme.yaml`](/guide/configuration), written to `/etc/lys-splash/theme.yaml` in the initrd and the system |

It cannot be combined with `boot.plymouth.enable` (an assertion fails). It
requires the systemd initrd: set `boot.initrd.systemd.enable = true` where that
is not the default.

## Ubuntu and Debian

Build the package's `compat` output with Nix (on any machine) and copy it under
`/usr`:

```sh
nix build github:stubbedev/lys-splash#default.compat
sudo apt-get remove plymouth
sudo cp -dRf result-compat/. /usr/
sudo systemctl enable lys-splash-start.service lys-splash-quit.service \
  lys-splash-quit-wait.service lys-splash-read-write.service \
  lys-splash-ask-password.path lys-splash-reboot.service \
  lys-splash-poweroff.service lys-splash-halt.service lys-splash-kexec.service
sudo update-initramfs -u
```

| Path in `compat` | Contents |
| --- | --- |
| `bin/lys-splash` | The binary |
| `bin/plymouth`, `sbin/plymouthd` | Symlinks to it that select the [role](/guide/command-line) |
| `lib/systemd/system/` | The [units](/guide/how-it-works#units), with `/usr` paths |
| `share/initramfs-tools/` | The hook (copies the binary, the links and `/etc/lys-splash/theme.yaml` into the image) and the `init-premount`, `init-bottom` and `panic` scripts |

cryptsetup's initramfs scripts call `plymouth` and so ask through the splash.

## Kernel command line

The units and the initramfs-tools script apply the same rule: the splash runs
when one of the "on" parameters is present and none of the "off" ones, in any
order.

| Parameter | Effect |
| --- | --- |
| `splash`, `splash=silent`, `rhgb` | On; one is required |
| `nosplash`, `splash=verbose`, `plymouth.enable=0` | Off |

When off, no lys-splash unit runs during boot or shutdown, and systemd's
console agent asks for passwords. The splash units also do not run in a
container. The NixOS module adds `splash`; on other systems add it to the
bootloader's command line.

## Check that it works

```sh
sudo lys-splash --ping
journalctl -b -u 'lys-splash*'
```

`--ping` exits 3 once the boot has finished, as no splash is running. A splash
started by a unit logs to the journal. One started by the initramfs-tools
script logs to `/dev/kmsg`: read it with `dmesg` or `journalctl -b -t
lys-splash`. The kernel rate-limits `/dev/kmsg`; boot with `printk.devkmsg=on`
to see all `--debug` output.
