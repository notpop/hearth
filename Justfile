# Hearth dev tasks. Run `just` (no args) to list them.
#
# Pair with `nix develop` (the flake provides Go 1.26 + tooling). The
# `just` binary itself comes from nixpkgs, so anyone who has Nix can run
# these without installing extra software.

# Default target: list available recipes.
default:
    @just --list

# ── core ──────────────────────────────────────────────────────────

# Build the all-in-one CLI binary into ./bin
build:
    mkdir -p bin
    go build -o bin/hearth ./cmd/hearth

# Run the full test suite
test:
    go test ./...

# Run with the race detector
test-race:
    go test -race ./...

# Coverage by package
cover:
    go test -coverprofile=/tmp/hearth-cov.out ./...
    go tool cover -func=/tmp/hearth-cov.out | tail -25

# Static checks
vet:
    go vet ./...

lint: vet
    staticcheck ./...

# Tidy go.mod / go.sum
tidy:
    go mod tidy

# Regenerate gRPC stubs from api/proto/
proto:
    protoc \
      --proto_path=api/proto \
      --go_out=. --go_opt=module=github.com/notpop/hearth \
      --go-grpc_out=. --go-grpc_opt=module=github.com/notpop/hearth \
      api/proto/hearth/v1/hearth.proto

# ── release ───────────────────────────────────────────────────────

# Cross-compile release artifacts for all targets into dist/
# Usage: just release-build v0.2.1-alpha
release-build version:
    rm -rf dist
    mkdir -p dist
    for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64; do \
        os="$${target%-*}"; \
        arch="$${target##*-}"; \
        ext=""; \
        [ "$$os" = "windows" ] && ext=".exe"; \
        out="dist/hearth-{{version}}-$${target}$${ext}"; \
        CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
            go build -trimpath -ldflags="-s -w" -o "$$out" ./cmd/hearth; \
        if [ "$$os" = "windows" ]; then \
            (cd dist && zip -q "hearth-{{version}}-$${target}.zip" "hearth-{{version}}-$${target}$${ext}"); \
        else \
            tar -C dist -czf "dist/hearth-{{version}}-$${target}.tar.gz" "hearth-{{version}}-$${target}$${ext}"; \
        fi; \
        rm -f "$$out"; \
    done
    cd dist && shasum -a 256 *.tar.gz *.zip > checksums.txt
    ls -la dist

# ── housekeeping ──────────────────────────────────────────────────

clean:
    rm -rf bin dist
