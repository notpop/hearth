{
  description = "Hearth — distributed task runner for your home network";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        go = pkgs.go;

        # Each task is a small shell script. They cd to the repo root
        # (the flake's project directory), then run their tool. Run any
        # task with `nix run .#<name>` (or `nix run .#<name> -- <args>`
        # to pass arguments).
        mkApp = name: body: {
          type = "app";
          program = "${pkgs.writeShellScript name ''
            set -euo pipefail
            cd "$(git -C "''${PWD:-.}" rev-parse --show-toplevel 2>/dev/null || echo "''${PRJ_ROOT:-$PWD}")"
            export PATH="${pkgs.lib.makeBinPath [
              go pkgs.protobuf pkgs.protoc-gen-go pkgs.protoc-gen-go-grpc
              pkgs.git pkgs.coreutils pkgs.gzip pkgs.gnutar pkgs.zip
              pkgs.go-tools pkgs.gotools
            ]}:$PATH"
            ${body}
          ''}";
        };

        scripts = {
          build = ''
            mkdir -p bin
            go build -o bin/hearth ./cmd/hearth
            echo "→ ./bin/hearth"
          '';
          test = ''go test ./...'';
          test-race = ''go test -race ./...'';
          cover = ''
            go test -coverprofile=/tmp/hearth-cov.out ./...
            go tool cover -func=/tmp/hearth-cov.out | tail -25
          '';
          vet = ''go vet ./...'';
          lint = ''
            go vet ./...
            staticcheck ./...
          '';
          tidy = ''go mod tidy'';
          proto = ''
            protoc \
              --proto_path=api/proto \
              --go_out=. --go_opt=module=github.com/notpop/hearth \
              --go-grpc_out=. --go-grpc_opt=module=github.com/notpop/hearth \
              api/proto/hearth/v1/hearth.proto
          '';

          # Convenience wrappers for the CLI.
          # nix run .#enroll -- --addr 192.168.1.10:7843 my-worker
          enroll = ''
            mkdir -p bin
            go build -o bin/hearth ./cmd/hearth
            ./bin/hearth enroll "$@"
          '';
          ca-init = ''
            mkdir -p bin
            go build -o bin/hearth ./cmd/hearth
            ./bin/hearth ca init "$@"
          '';
          coordinator = ''
            mkdir -p bin
            go build -o bin/hearth ./cmd/hearth
            ./bin/hearth coordinator "$@"
          '';

          # Cross-compile release artifacts.
          # nix run .#release-build -- v0.2.1-alpha
          release-build = ''
            version="''${1:?usage: nix run .#release-build -- <version>}"
            rm -rf dist && mkdir -p dist
            for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64; do
              os="''${target%-*}"
              arch="''${target##*-}"
              ext=""
              [ "$os" = "windows" ] && ext=".exe"
              out="dist/hearth-$version-''${target}''${ext}"
              CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
                go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/hearth
              if [ "$os" = "windows" ]; then
                (cd dist && zip -q "hearth-$version-''${target}.zip" "hearth-$version-''${target}''${ext}")
              else
                tar -C dist -czf "dist/hearth-$version-''${target}.tar.gz" "hearth-$version-''${target}''${ext}"
              fi
              rm -f "$out"
            done
            (cd dist && shasum -a 256 *.tar.gz *.zip > checksums.txt)
            ls -la dist
          '';

          clean = ''rm -rf bin dist'';
        };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            go-tools
            golangci-lint
            delve
            sqlite
            protobuf
            protoc-gen-go
            protoc-gen-go-grpc
            grpcurl
          ];

          shellHook = ''
            export GOPATH="$PWD/.gopath"
            export GOBIN="$GOPATH/bin"
            export PATH="$GOBIN:$PATH"
            mkdir -p "$GOBIN"

            # Define a shell function for each task so users can just type
            # `build`, `test`, `vet`, etc. inside this shell — no
            # `nix run .#<name>` ceremony required. Functions are resolved
            # before builtins (so our `test` shadows the POSIX `test`
            # builtin in interactive use; fall back to `command test`
            # if you need the builtin).
            ${pkgs.lib.concatStringsSep "\n" (pkgs.lib.mapAttrsToList (name: _: ''
              ${name}() { nix run ".#${name}" -- "$@"; }
            '') scripts)}

            echo "hearth dev shell — $(go version)"
            echo "tasks: ${pkgs.lib.concatStringsSep " " (builtins.attrNames scripts)}"
            echo "  in-shell:  build    test    enroll <name>"
            echo "  outside:   nix run .#build    nix run .#test    nix run .#enroll -- <name>"
          '';
        };

        # Each script becomes a `nix run .#name`-able app.
        apps = builtins.mapAttrs (name: body: mkApp name body) scripts;

        # Hearth CLI as a Nix package; lets other flakes consume it via
        # `inputs.hearth.url = "github:notpop/hearth";` and reference
        # `hearth.packages.${system}.default`.
        packages.default = pkgs.buildGoModule {
          pname = "hearth";
          version = "0.2.1-alpha";
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/hearth" ];
          env.CGO_ENABLED = "0";
          ldflags = [ "-s" "-w" ];
        };

        formatter = pkgs.nixpkgs-fmt;
      }
    );
}
