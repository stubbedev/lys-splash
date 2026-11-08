<p align="center">
  <img src="docs/public/lys-splash.svg" width="160" alt="lys-splash">
</p>

<h1 align="center">lys-splash</h1>

<p align="center">
  The lys desktop's boot splash, in place of Plymouth. It starts in the
  initrd, draws on every output, asks for disk passwords, survives the
  switch to the real root and hands its last frame to the display manager.
</p>

<p align="center">
  <a href="https://github.com/stubbedev/lys-splash/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/stubbedev/lys-splash/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="MIT license"></a>
  <a href="https://stubbedev.github.io/lys-splash/"><img src="https://img.shields.io/badge/docs-stubbedev.github.io-informational?style=for-the-badge" alt="Documentation"></a>
</p>

<p align="center">
  <img src="internal/theme/testdata/golden/boot-progress-50.png" width="480" alt="The boot splash">
</p>

It speaks plymouthd's socket protocol and answers to plymouth's command line,
so systemd, its password agents and cryptsetup's scripts work with it
unchanged. It is one static, pure-Go binary with its theme, font and logo
embedded.

## Install

NixOS:

```nix
{
  imports = [lys-splash.nixosModules.default];
  boot.lys-splash.enable = true;
}
```

Ubuntu and Debian: copy the package's `compat` output under `/usr`; see the
[install guide](https://stubbedev.github.io/lys-splash/guide/install).

## Use

The next boot shows the splash. Esc toggles the boot messages.
`plymouth.enable=0` on the kernel command line boots without it. The theme is
`/etc/lys-splash/theme.yaml`:

```yaml
accent: "#7aa2f7"
throbber: dots
```

Do not start the daemon on a running desktop: it takes over the display.

## Documentation

[stubbedev.github.io/lys-splash](https://stubbedev.github.io/lys-splash/)

## The lys family

[lys-shell](https://github.com/stubbedev/lys-shell),
[lys-wm](https://github.com/stubbedev/lys-wm),
[lys-ui](https://github.com/stubbedev/lys-ui),
[lys-lib](https://github.com/stubbedev/lys-lib),
[lys-login](https://github.com/stubbedev/lys-login),
[lys-polkit](https://github.com/stubbedev/lys-polkit),
[lys-secrets](https://github.com/stubbedev/lys-secrets).

## License

MIT. The embedded font is Cantarell Regular under the SIL Open Font License 1.1
(`internal/theme/assets/LICENSE-Cantarell.txt`); the logo is lys's.
