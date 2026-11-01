# Development

Every dev tool comes from the flake: the Go toolchain, gopls, golangci-lint,
delve, just, git and gh. Use `direnv allow` on entry, or `nix develop`
explicitly. `GOTOOLCHAIN=local` keeps the compiler from downloading itself.

## Layout

| Path | What it is |
| --- | --- |
| `main.go` | Picks the role from the command line, and starts the daemon or the client |
| `internal/protocol` | plymouthd's socket protocol: requests and responses, encoded and decoded |
| `internal/server` | The daemon side: the abstract socket, peer credentials, requests answered now or later |
| `internal/client` | The plymouth command line |
| `internal/daemon` | The splash process: the protocol's lifecycle from boot to quit, the modes and the boot duration |
| `internal/theme` | The validated `theme.yaml` (default embedded) and the frame drawn from the splash's state with lys-lib's canvas, damage included |
| `internal/kms` | Every card's outputs on two dumb buffers each: mode and scale choice, panel rotation, paced damage-limited flips, cards followed through kernel uevents |
| `internal/console` | The virtual terminal, the console's captured output and the text fallback |
| `internal/key` | The bytes the terminal sends, decoded into keys |
| `internal/ask` | The queue of prompts, the text typed, the cached passwords and the keystroke watches |
| `units/` | The systemd units, with `@placeholders@` the package fills in |
| `initramfs-tools/` | The hook and scripts for Ubuntu and Debian |
| `nix/` | The package, the NixOS module, the dev shell and the checks |

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

## Nix

- `nix build` builds the package (`nix/package.nix`). `vendorHash` changes with
  go.sum.
- `nix fmt` formats the nix files with alejandra.
- `nix flake check` runs the checks:

| Check | What it does |
| --- | --- |
| `package` | Builds the package and runs its unit tests |
| `plymouth-client` | nixpkgs' `plymouth` client against the server |
| `units` | Every unit conflicts with its Plymouth unit, and the units and initramfs-tools' script agree on a table of kernel command lines |
| `module` | Evaluates the NixOS module and looks for the splash in its initrd |
| `vm-kms` | A VM whose only card is vkms runs the kms package's real-card test as root |
| `vm-kms-switch` | The splash on simpledrm and then on the native driver as it takes over, in a VM |
| `vm-splash` | The daemon on virtio-gpu, with the plymouth client and systemd's password agent |
| `vm-boot` | A boot through the module, on virtio-gpu and without any display |
| `vm-greeter` | Hands the splash's frame to lys-login's greeter on lys-wm and checks every screen dump on the way |
| `vm-e2e` | The whole path in one test, each stage failing with its name: OVMF and simpledrm, then virtio-gpu, a LUKS root and a second LUKS disk unlocked by one passphrase typed at the splash, the splash surviving the switch to the real root, nixpkgs' plymouth client, the hand-over to the greeter, the reboot splash, and a machine without a display typing its passphrase at the text fallback |

The expected frames are in `internal/theme/testdata/golden`; the pictures on this
site are those files.

## This site

The site is in `docs/`, on the theme all lys sites share. Node is not in the dev
shell:

```sh
cd docs
nix shell nixpkgs#nodejs_22 -c npm install
nix shell nixpkgs#nodejs_22 -c npm run build
nix shell nixpkgs#nodejs_22 -c npm run preview
```

`.github/workflows/docs.yml` publishes it on every push to `main`.

## CI

`.github/workflows/ci.yml` runs the gates, the `package`, `units`, `module` and
`plymouth-client` checks, the initramfs-tools image in Ubuntu, and the VM checks
`vm-e2e`, `vm-greeter` and `vm-boot` under KVM.

`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning; dependency review gates pull
requests. Dependabot keeps the module and the workflows current.

## Linting

`.golangci.yml` starts from the standard linter set and layers on bodyclose,
copyloopvar, errorlint, gosec, intrange, misspell, modernize, nolintlint,
perfsprint, revive, unconvert, unparam, usestdlibvars and whitespace, with gofumpt
and gci as formatters. No issue caps: every finding fails the build.
