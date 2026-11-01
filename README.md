<p align="center">
  <img src="docs/public/lys-splash.svg" width="160" alt="lys-splash">
</p>

<h1 align="center">lys-splash</h1>

<p align="center">
  The lys desktop's boot splash, in place of Plymouth. It starts in the
  initrd, draws on every output, asks for disk passwords, survives the
  switch to the real root, and leaves its last frame on screen for
  lys-login's greeter to take over without a flicker.
</p>

<p align="center">
  <a href="https://github.com/stubbedev/lys-splash/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/stubbedev/lys-splash/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="MIT license"></a>
  <a href="https://stubbedev.github.io/lys-splash/"><img src="https://img.shields.io/badge/docs-stubbedev.github.io-informational?style=for-the-badge" alt="Documentation"></a>
</p>

<p align="center">
  <img src="internal/theme/testdata/golden/boot-progress-50.png" width="480" alt="The boot splash">
</p>

It speaks plymouthd's socket protocol, so systemd and its password agents
talk to it unchanged. It is one static, pure-Go binary (`CGO_ENABLED=0`)
with its theme embedded, so the initrd carries no libraries for it.

## Install

On NixOS, add the flake as an input and import its module:

```nix
{
  imports = [lys-splash.nixosModules.default];
  boot.lys-splash.enable = true;
}
```

On Ubuntu and Debian, the package's `compat` output replaces plymouth under
`/usr`. The [install guide](https://stubbedev.github.io/lys-splash/guide/install)
has the steps.

## Use

There is nothing to run: the next boot shows the splash. Esc shows the boot
messages. Add `plymouth.enable=0` to the kernel command line to boot without
it. Colours, the logo and the throbber are set in
`/etc/lys-splash/theme.yaml`:

```yaml
accent: "#7aa2f7"
throbber: dots
```

Do not start the daemon on a machine you are using: it takes over the display.

## Documentation

[stubbedev.github.io/lys-splash](https://stubbedev.github.io/lys-splash/) has
the [configuration](https://stubbedev.github.io/lys-splash/guide/configuration),
the [command line](https://stubbedev.github.io/lys-splash/guide/command-line),
[how it works](https://stubbedev.github.io/lys-splash/guide/how-it-works),
including the contract a display manager follows to take the screen over, and
the [development](https://stubbedev.github.io/lys-splash/guide/development)
recipes and checks.

## The lys family

lys-splash is part of the lys desktop, with
[lys-shell](https://github.com/stubbedev/lys-shell),
[lys-wm](https://github.com/stubbedev/lys-wm),
[lys-ui](https://github.com/stubbedev/lys-ui),
[lys-lib](https://github.com/stubbedev/lys-lib),
[lys-login](https://github.com/stubbedev/lys-login),
[lys-polkit](https://github.com/stubbedev/lys-polkit) and
[lys-secrets](https://github.com/stubbedev/lys-secrets).

## License

MIT. The embedded font is Cantarell Regular under the SIL Open Font License 1.1
(`internal/theme/assets/LICENSE-Cantarell.txt`); the logo is lys's.
