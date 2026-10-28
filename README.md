# lys-splash

lys-splash is the lys desktop's boot splash, in place of Plymouth. It
starts in the initrd, draws on every output through KMS dumb buffers,
asks for disk passwords, follows the boot from systemd's unit status,
survives the switch to the real root, and leaves its last frame on
screen for lys-login's greeter to take over without a flicker. It
speaks plymouthd's socket protocol, so systemd and its password agents
talk to it unchanged.

One static, pure-Go binary (`CGO_ENABLED=0`) with its theme embedded,
so the initrd carries no libraries for it.

The implementation is tracked in the repository's issues.

## Layout

| Package | What it is |
| --- | --- |
| `internal/protocol` | plymouthd's socket protocol: requests and responses, encoded and decoded |
| `internal/server` | The daemon side: the abstract socket, peer credentials, requests answered now or later |
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
| `just fmt` | Format every Go source in place |

Every dev tool (Go toolchain pinned to the version go.mod declares, gopls,
golangci-lint, delve, just, git, gh) comes from the flake: `direnv allow`
on entry, or `nix develop` explicitly. `GOTOOLCHAIN=local` keeps the compiler
from downloading itself.

## Nix

- `nix build` builds the package (`nix/package.nix`; the dev shell is
  `nix/shell.nix` and the checks `nix/checks.nix`, all imported by
  `flake.nix`; `vendorHash` changes with go.sum)
- `nix flake check` runs the checks, among them `plymouth-client` and
  `vm-kms`, a NixOS VM whose only card is vkms that runs the kms
  package's real-card test as root
- `nix run` runs it
- `nix fmt` formats the nix files with alejandra

## CI

`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning; dependency review gates
pull requests. Dependabot keeps the module and the workflows current.

## Linting

`.golangci.yml` starts from the standard linter set and layers on bodyclose,
copyloopvar, errorlint, gosec, intrange, misspell, modernize, nolintlint,
perfsprint, revive, unconvert, unparam, usestdlibvars and whitespace, with
gofumpt and gci as formatters. No issue caps: every finding fails the build.
