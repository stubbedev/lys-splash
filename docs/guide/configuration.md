# Configuration and themes

The theme is the splash's only configuration file: `/etc/lys-splash/theme.yaml`,
or the file `--theme` names. Without one the splash uses the theme embedded in
the binary. A field left out keeps the embedded theme's value.

| Field | Values | Embedded value | What it sets |
| --- | --- | --- | --- |
| `background` | `#rrggbb` | `#000000` | The background |
| `foreground` | `#rrggbb` | `#d8d9e6` | The message and prompt text |
| `accent` | `#rrggbb` | `#ffb454` | The throbber, the progress bars and the prompt's border |
| `logo` | `firmware`, `lys` | `firmware` | `firmware` shows the firmware's own logo when it left one, else lys's; `lys` always shows lys's |
| `throbber` | `spinner`, `dots` | `spinner` | The busy animation |
| `units` | `true`, `false` | `false` | Shows the name of each unit systemd starts under the throbber |

The background stays black in the embedded theme so the firmware's logo, drawn
on black, does not change underneath. The logo and the font cannot be replaced
from the theme, and the scale is chosen per output, not configured.

```yaml
accent: "#7aa2f7"
throbber: dots
units: true
```

`throbber: dots` looks like this:

![The boot splash with three dots as its throbber](../../internal/theme/testdata/golden/boot-dots.png)

## NixOS

`boot.lys-splash.theme` holds the same fields. The module writes them to
`/etc/lys-splash/theme.yaml` in both the initrd and the system:

```nix
{
  boot.lys-splash.theme = {
    accent = "#7aa2f7";
    throbber = "dots";
  };
}
```

The option takes any YAML. Its fields are checked when the splash reads them,
not when the system is built.

## Ubuntu and Debian

The hook copies `/etc/lys-splash/theme.yaml` into the image when it builds one,
so run `sudo update-initramfs -u` after changing the file.

## A theme that does not validate

A theme with an unknown field, a colour that is not `#rrggbb` or a value not on
its list is rejected whole. The splash logs a warning naming the file, the line
and the field, and draws the embedded theme, because a boot should not fail over
its colours. On a systemd initrd, look for it in the journal:

```sh
journalctl -b -u lys-splash-start.service
```

A splash that initramfs-tools' script starts logs to the kernel's buffer instead:

```sh
journalctl -b -t lys-splash
```

## Trying a change

`lys-splash reload` makes a running splash read its theme file again, with the
same fallback to the embedded theme. It is only useful while a splash shows, so
try themes in a VM.
