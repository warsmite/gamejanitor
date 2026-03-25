{
  description = "Gamejanitor - local game server hosting tool";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      packages.${system} = let
        ui = pkgs.buildNpmPackage {
          pname = "gamejanitor-ui";
          version = "0.1.0";
          src = ./.;
          postUnpack = ''
            sourceRoot="$sourceRoot/ui"
          '';
          npmDepsHash = "sha256-4ylyfmqUiQDOVGit3MUC8Bh0NHQ/3U2aU9HWQJvIXAY=";
          installPhase = ''
            cp -r dist $out
          '';
        };
      in {
        default = pkgs.buildGoModule {
          pname = "gamejanitor";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-V4+EkdNJczWSyThv5ka1sLQQa9CBoI/IWnDr5Kppecc=";
          env.CGO_ENABLED = "0";

          preBuild = ''
            cp -r ${ui} ui/dist
          '';
        };
      };

      nixosModules.default = import ./nixos/module.nix self;

      devShells.${system}.default =
        let
          dev = pkgs.writeShellScriptBin "dev" ''
            # Watch Go files and game data, restart server on change
            reflex -s -r '\.(go|yaml)$' -R 'node_modules' -- go run . serve -d /tmp/gamejanitor-data "$@"
          '';

          cli = pkgs.writeShellScriptBin "cli" ''
            exec go run . "$@"
          '';

          # TODO: migrate to ghcr.io/gamejanitor when going public
          build-image = pkgs.writeShellScriptBin "build-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: build-image <base|steamcmd|java|dotnet>"
              exit 1
            fi
            docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: push-image <base|steamcmd|java|dotnet>"
              exit 1
            fi
            echo "Building and pushing $image..."
            docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
            docker push "ghcr.io/warsmite/gamejanitor/$image"
          '';

          push-all-images = pkgs.writeShellScriptBin "push-all-images" ''
            # Build order matters: base must be built first since others depend on it
            for image in base steamcmd java dotnet; do
              echo "Building and pushing $image..."
              docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
              docker push "ghcr.io/warsmite/gamejanitor/$image"
            done
          '';

          # Multi-node test scripts — separate data dirs so they don't conflict
          dev-controller = pkgs.writeShellScriptBin "dev-controller" ''
            echo "Starting controller on :8090 (gRPC :9090)"
            exec go run . serve \
              --controller --worker=false \
              --port 8090 --grpc-port 9090 --sftp-port 2022 \
              -d /tmp/gamejanitor-controller "$@"
          '';

          dev-worker = pkgs.writeShellScriptBin "dev-worker" ''
            PORT=''${1:-9091}
            CTRL=''${2:-localhost:9090}
            TOKEN=''${3:-}
            EXTRA_ARGS=()
            if [ -n "$TOKEN" ]; then
              EXTRA_ARGS+=(--worker-token "$TOKEN")
            fi
            echo "Starting worker agent on gRPC :$PORT, registering with controller at $CTRL"
            exec go run . serve \
              --worker --controller=false \
              --grpc-port "$PORT" \
              --controller-address "$CTRL" \
              -d "/tmp/gamejanitor-worker-$PORT" "''${EXTRA_ARGS[@]}" "''${@:4}"
          '';

          gen-proto = pkgs.writeShellScriptBin "gen-proto" ''
            protoc --go_out=. --go_opt=module=github.com/warsmite/gamejanitor \
                   --go-grpc_out=. --go-grpc_opt=module=github.com/warsmite/gamejanitor \
                   proto/worker.proto
          '';

          build = pkgs.writeShellScriptBin "build" ''
            set -e
            echo "Building UI..."
            cd ui && npm run build && cd ..
            echo "Building Go binary..."
            go clean -cache
            go build -o gamejanitor .
            echo "Done: ./gamejanitor"
          '';

          test = pkgs.writeShellScriptBin "test" ''
            exec go test ./... "$@"
          '';

          test-all = pkgs.writeShellScriptBin "test-all" ''
            exec go test -tags integration ./... "$@"
          '';

          test-race = pkgs.writeShellScriptBin "test-race" ''
            exec go test -race ./... "$@"
          '';

          test-e2e = pkgs.writeShellScriptBin "test-e2e" ''
            echo "Building gamejanitor..."
            go build -o /tmp/gamejanitor-e2e .
            echo "Running e2e tests (requires Docker/Podman + base image)..."
            exec go test -tags e2e -timeout 5m -v ./e2e/ "$@"
          '';

          test-smoke = pkgs.writeShellScriptBin "test-smoke" ''
            echo "Building gamejanitor..."
            go build -o /tmp/gamejanitor-e2e .
            echo "Running smoke tests (SMOKE_GAME=''${SMOKE_GAME:-terraria})..."
            exec go test -tags smoke -timeout 15m -v ./e2e/ "$@"
          '';

          test-coverage = pkgs.writeShellScriptBin "test-coverage" ''
            set -e
            go test -coverprofile=/tmp/gamejanitor-coverage.out ./service/ ./models/ ./api/handlers/ ./games/ ./worker/ ./naming/ "$@"
            echo ""
            echo "=== Per-package coverage ==="
            go tool cover -func=/tmp/gamejanitor-coverage.out | grep "^total:"
            echo ""
            echo "=== Per-package breakdown ==="
            go test -cover ./service/ ./models/ ./api/handlers/ ./games/ ./worker/ ./naming/ 2>&1 | grep "coverage:"
            echo ""
            echo "HTML report: go tool cover -html=/tmp/gamejanitor-coverage.out"
          '';

          cleanup = pkgs.writeShellScriptBin "cleanup" ''
            for runtime in docker podman; do
              if ! command -v "$runtime" &>/dev/null; then
                continue
              fi
              # Try rootless first
              if $runtime info &>/dev/null; then
                echo "Cleaning up $runtime containers..."
                $runtime ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r $runtime rm -f
                echo "Cleaning up $runtime volumes..."
                $runtime volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r $runtime volume rm -f
              fi
              # Try rootful if sudo is available
              if sudo -n true 2>/dev/null && sudo $runtime info &>/dev/null; then
                echo "Cleaning up $runtime containers (rootful)..."
                sudo $runtime ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r sudo $runtime rm -f
                echo "Cleaning up $runtime volumes (rootful)..."
                sudo $runtime volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r sudo $runtime volume rm -f
              fi
            done
            echo "Removing /tmp/gamejanitor-*..."
            rm -rf /tmp/gamejanitor-data /tmp/gamejanitor-controller /tmp/gamejanitor-worker-*
            echo "Cleanup complete."
          '';
        in
        pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.docker-client
            pkgs.reflex
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
            pkgs.nodejs
            dev
            cli
            build
            build-image
            push-image
            push-all-images
            dev-controller
            dev-worker
            gen-proto
            cleanup
            test
            test-all
            test-race
            test-e2e
            test-smoke
            test-coverage
          ];

          shellHook = ''
            export CGO_ENABLED=0
          '';
        };

    };
}
