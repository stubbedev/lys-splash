# lys-splash dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Run every release gate in order: vet, lint, test, build.
check: vet lint test build

# Static analysis of every package with go vet.
vet:
    go vet ./...

# Lint every package; settings live in .golangci.yml.
lint:
    golangci-lint run

# Run the test suite for every package.
test:
    go test ./...

# Compile-check every package; the output is discarded.
build:
    go build -o /dev/null ./...

# Run nixpkgs' plymouth client against the server, in the nix sandbox.
plymouth-client:
    nix build --no-link -L .#checks.{{ arch() }}-{{ os() }}.plymouth-client

# Build an initramfs-tools image with the compat output in an Ubuntu container.
initramfs-tools image="ubuntu:24.04":
    ./nix/initramfs-tools-check.sh {{image}}

# Format every Go source in place with gofmt.
fmt:
    gofmt -w .

# Set nix/package.nix's vendorHash to the hash of the modules go.sum pins.
vendor-hash:
    nix run github:stubbedev/lys-ci#vendor-hash -- nix/package.nix .#default.goModules
