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
            echo "hearth dev shell — $(go version)"
          '';
        };

        formatter = pkgs.nixpkgs-fmt;
      }
    );
}
