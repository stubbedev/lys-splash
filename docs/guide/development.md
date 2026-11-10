# Development

The flake's dev shell (`direnv allow` or `nix develop`) provides Go, gopls,
golangci-lint, delve, just, git, gh and the lys-ci tools, with
`CGO_ENABLED=0` and `GOTOOLCHAIN=local`.

## Layout

| Path | Contents |
| --- | --- |
| `main.go` | Role selection; starts the daemon or the client |
| `internal/protocol` | plymouthd's socket protocol: request and response codec |
| `internal/server` | Daemon side of the socket: peer credentials, deferred answers |
| `internal/client` | The plymouth command line |
| `internal/daemon` | Splash lifecycle from boot to quit, modes, boot duration |
| `internal/theme` | `theme.yaml` validation (default embedded), BGRT logo, frame rendering with damage |
| `internal/kms` | Outputs on two dumb buffers per card: mode and scale, rotation, paced damage-limited flips, uevents |
| `internal/console` | Virtual terminal, captured console output, text fallback |
| `internal/key` | Terminal bytes decoded into keys |
| `internal/keymap` | Console keymap from `vconsole.conf` or `default/keyboard` |
| `internal/ask` | Prompt queue, typed text, cached passwords, keystroke watches |
| `internal/kmsg` | Log handler writing to `/dev/kmsg` for the detached daemon |
| `internal/asynclog` | Log handler that never blocks the caller |
| `units/` | systemd units with `@placeholders@` filled in by the package |
| `initramfs-tools/` | Hook and scripts for Ubuntu and Debian |
| `nix/` | Package, NixOS module, dev shell, checks |

## Recipes

| Recipe | Effect |
| --- | --- |
| `just check` | All gates in order: vet, lint, no-fused, test, build |
| `just vet` | `go vet ./...` |
| `just lint` | `golangci-lint run` with `.golangci.yml` |
| `just no-fused` | Fails on float products fused into FMA, which round differently from amd64 |
| `just test` | `go test ./...` |
| `just build` | Compile-checks every package |
| `just fmt` | `gofmt -w .` |
| `just plymouth-client` | Runs the `plymouth-client` check |
| `just initramfs-tools [image]` | Builds an initramfs-tools image from the compat output in an Ubuntu container (default `ubuntu:24.04`) and checks it holds the binary, links and scripts in order; needs Docker. It does not boot the image |
| `just vendor-hash` | Updates `vendorHash` in `nix/package.nix` from `go.sum` |
| `just release-patch`, `release-minor`, `release-major` | Tags and pushes the next release from a clean default branch with green CI |

## Nix

- `nix build`: the package (`nix/package.nix`), outputs `out` and `compat`.
- `nix fmt`: alejandra.
- `nix flake check` runs:

| Check | Tests |
| --- | --- |
| `package` | Builds the package and runs the unit tests |
| `plymouth-client` | nixpkgs' `plymouth` client against the server |
| `units` | Every unit conflicts with its Plymouth unit; units and initramfs-tools script agree on a table of kernel command lines |
| `module` | Evaluates the NixOS module and finds the splash in the initrd |
| `vm-kms` | The kms package's real-card test as root on a vkms-only VM |
| `vm-kms-switch` | The splash on simpledrm, then on the native driver as it takes over |
| `vm-splash` | The daemon on virtio-gpu with the plymouth client and systemd's password agent, then detached as `plymouthd` logging to the journal |
| `vm-boot` | A boot through the module, on virtio-gpu and with no display |
| `vm-journal` | Reading a guest's whole journal through a slowly drained VM shell |
| `vm-greeter` | Hand-over to lys-login's greeter on lys-wm, checking every screen dump |
| `vm-greeter-outputs` | Hand-over to the greeter with two vkms outputs, both lit by the splash and both taken by the greeter |
| `vm-e2e` | The full path, each stage failing by name: OVMF and simpledrm, virtio-gpu, a LUKS root and a second LUKS disk unlocked by one passphrase at the splash, switch root, the plymouth client, hand-over to the greeter, the reboot splash, and a display-less machine typing its passphrase at the text fallback |

virtio-gpu and vkms have no Vulkan driver, so `nix/greeter.nix` sets
`LYS_WM_RENDERER=gles` in the session variables, which reach the greeter's
lys-wm through PAM: it renders with GLES on llvmpipe instead of stopping.

Expected frames live in `internal/theme/testdata/golden`; this site's pictures
are those files.

## CI

`.github/workflows/ci.yml` runs `just check`; the `package`, `units`, `module`
and `plymouth-client` checks; the initramfs-tools image; and `vm-e2e`,
`vm-greeter`, `vm-boot`, `vm-splash` and `vm-journal` under KVM.
`.github/workflows/security.yml` runs CodeQL, Grype and govulncheck, and
dependency review on pull requests. Dependabot updates Go modules, actions and the site's npm packages.

## This site

The site in `docs/` uses the shared lys theme. Node is not in the dev shell:

```sh
cd docs
nix shell nixpkgs#nodejs_22 -c npm ci
nix shell nixpkgs#nodejs_22 -c npm run build
nix shell nixpkgs#nodejs_22 -c npm run preview
```

`.github/workflows/docs.yml` publishes it on pushes to `main` that touch `docs/`, the workflow or the golden frames.
