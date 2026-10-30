# lys-splash

lys-splash is the lys desktop's boot splash, in place of Plymouth. It
starts in the initrd, draws on every output through KMS dumb buffers,
asks for disk passwords, follows the boot from systemd's unit status,
survives the switch to the real root, and leaves its last frame on
screen for lys-login's greeter to take over without a flicker. It
speaks plymouthd's socket protocol, so systemd and its password agents
talk to it unchanged.

One static, pure-Go binary (`CGO_ENABLED=0`) with its theme embedded,
so the initrd carries no libraries for it. The embedded font is
Cantarell Regular under the SIL Open Font License 1.1
(`internal/theme/assets/LICENSE-Cantarell.txt`); the logo is lys's.

The implementation is tracked in the repository's issues.

## Installing

### NixOS

Add the flake as an input and import its module:

```nix
{
  imports = [lys-splash.nixosModules.default];
  boot.lys-splash = {
    enable = true;
    theme = {throbber = "dots";};
  };
}
```

`boot.lys-splash.enable` puts the binary, the theme and the units into the
systemd initrd and the system, and adds `splash` to the kernel command
line. It cannot be combined with `boot.plymouth.enable`, which an
assertion names. `boot.lys-splash.theme` holds `theme.yaml`'s fields;
those left out keep the embedded theme's.

### Ubuntu and Debian, in place of plymouth

The `compat` output is a tree to copy under `/usr`: `bin/lys-splash`, the
`bin/plymouth` and `sbin/plymouthd` links to it, the systemd units with
`/usr` paths, and initramfs-tools' hook and `init-premount`,
`init-bottom` and `panic` scripts, which do what plymouth's do under the
same command-line rules (`splash`, `nosplash`, `plymouth.enable=0`).
cryptsetup's initramfs scripts call `plymouth`, so they ask through the
splash unchanged.

```sh
nix build .#default.compat
sudo apt-get remove plymouth
sudo cp -a --no-preserve=ownership result-compat/. /usr/
sudo systemctl enable lys-splash-start.service lys-splash-quit.service \
  lys-splash-quit-wait.service lys-splash-read-write.service \
  lys-splash-ask-password.path lys-splash-reboot.service \
  lys-splash-poweroff.service lys-splash-halt.service lys-splash-kexec.service
sudo update-initramfs -u
```

A theme at `/etc/lys-splash/theme.yaml` goes into the image too. Run as
`plymouthd`, the splash detaches once it is ready, as plymouthd does, and
exits 0 when a splash is already running, so the initrd's splash carries
on into the real root.

## Handing the screen over

The kernel's fbdev emulation (`drm_kms_helper.fbdev_emulation=1`, the
default) restores its console's framebuffer with a full modeset whenever
the last open file of a card closes (`drm_lastclose` →
`drm_client_dev_restore`). Closing the splash's framebuffer with
`CLOSEFB` does not prevent it: if lys-splash holds the last fd on a card
when it exits, the console's frame replaces the splash's before the next
compositor opens the card.

lys-splash therefore hands over in plymouth's and GDM's order, so its
close is never the last one:

1. `lys-splash deactivate` drops DRM master on every card and keeps the
   cards open, with the frame on screen.
2. The compositor opens the cards and takes master without a modeset.
3. `lys-splash quit --retain-splash` closes the splash's framebuffers with
   `CLOSEFB` and exits; the compositor's fd keeps the card open, so
   nothing restores the console.

A display manager follows that order through this contract, as GDM does
with plymouth:

- Its unit has `After=lys-splash-start.service getty@tty1.service
  lys-splash-quit.service` and `Conflicts=lys-splash-quit.service
  getty@tty1.service`, so systemd neither quits the splash nor puts tty1
  back into text mode under it.
- Before it starts its compositor it runs `lys-splash deactivate`
  (`ExecStartPre=-lys-splash deactivate`; without a splash the call fails
  and is ignored).
- Once its first frame has been presented (`wp_presentation` feedback), it
  runs `lys-splash quit --retain-splash`.

lys-login's greeter, on lys-wm, which keeps an inherited frame without a
modeset, implements it. With no display manager, `lys-splash-quit.service`
quits the splash at the end of the boot without retain, and the console
comes back with the boot messages the splash captured.

Neither of the alternatives is used, and the NixOS module leaves fbdev
emulation on: turning it off removes the kernel console from every
card, and parking the card fds in systemd's fd store keeps the card open
after the compositor exits, so the console would never come back. The
shutdown, reboot and kexec splashes never close their cards: systemd
spares them (argv[0] starts with `@`) and the power goes with their frame
still on screen.

## Layout

| Package | What it is |
| --- | --- |
| `internal/protocol` | plymouthd's socket protocol: requests and responses, encoded and decoded |
| `internal/server` | The daemon side: the abstract socket, peer credentials, requests answered now or later |
| `internal/theme` | The validated `theme.yaml` (default embedded) and the frame drawn from the splash's state with lys-lib's canvas, damage included: the firmware's BGRT logo or lys's, a throbber with the boot's progress under it or the update's progress bar, the message line and the prompt |
| `internal/kms` | Every card's outputs on two dumb buffers each: mode and scale choice, panel rotation, paced damage-limited flips, cards followed through kernel uevents |

## Recipes

| Recipe | What it does |
| --- | --- |
| `just check` | Every release gate in order: vet, lint, test, build |
| `just vet` | Static analysis with `go vet` |
| `just lint` | `golangci-lint run` against the strict config in `.golangci.yml` |
| `just test` | `go test ./...` |
| `just build` | Compile-check every package |
| `just plymouth-client` | Run nixpkgs' `plymouth` client against the server (`checks.plymouth-client`) |
| `just initramfs-tools` | Install the compat output into an Ubuntu container, build an initramfs-tools image and check that it holds the splash, the links and the scripts (needs Docker) |
| `just fmt` | Format every Go source in place |

Every dev tool (Go toolchain pinned to the version go.mod declares, gopls,
golangci-lint, delve, just, git, gh) comes from the flake: `direnv allow`
on entry, or `nix develop` explicitly. `GOTOOLCHAIN=local` keeps the compiler
from downloading itself.

## Nix

- `nix build` builds the package (`nix/package.nix`; the dev shell is
  `nix/shell.nix` and the checks `nix/checks.nix`, all imported by
  `flake.nix`; `vendorHash` changes with go.sum)
- `nix flake check` runs the checks, among them `plymouth-client`,
  `module`, which evaluates the NixOS module and looks for the splash in
  its initrd, `vm-kms`, a NixOS VM whose only card is vkms that runs the
  kms package's real-card test as root, `vm-boot`, which boots through
  the module, `vm-greeter`, which hands the splash's frame to lys-login's
  greeter on lys-wm and checks every screen dump on the way, and `vm-e2e`,
  the whole path in one test: OVMF and simpledrm, then virtio-gpu, a LUKS
  root and a second LUKS disk unlocked by one passphrase typed at the
  splash, the splash surviving the switch to the real root, nixpkgs'
  plymouth client, the hand-over to the greeter, the reboot splash, and a
  machine without a display typing its passphrase at the text fallback;
  each stage fails with its name
- `nix run` runs it
- `nix fmt` formats the nix files with alejandra

## CI

`.github/workflows/ci.yml` runs the gates, the package, unit and module
checks, the initramfs-tools image in Ubuntu, and the VM checks `vm-e2e`,
`vm-greeter` and `vm-boot` under KVM.

`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning; dependency review gates
pull requests. Dependabot keeps the module and the workflows current.

## Linting

`.golangci.yml` starts from the standard linter set and layers on bodyclose,
copyloopvar, errorlint, gosec, intrange, misspell, modernize, nolintlint,
perfsprint, revive, unconvert, unparam, usestdlibvars and whitespace, with
gofumpt and gci as formatters. No issue caps: every finding fails the build.
