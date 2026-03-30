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
          src = ./ui;
          npmDepsHash = "sha256-b09AEsgcy52kcGj7rMuriVJcSimjZRzxTB0BOSvqY+w=";
          installPhase = ''
            cp -r dist $out
          '';
        };
      in {
        default = pkgs.buildGoModule {
          pname = "gamejanitor";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-Aakk1e8WQvw2KmmkshQ6Nswb6WzWuupGEenq4vcLps8=";
          env.CGO_ENABLED = "0";

          # sdk/ and games/ are separate Go modules with their own go.mod — exclude from main build
          excludedPackages = [ "sdk" "games" ];

          # e2e tests need Docker + a built binary; netutil DNS tests need network.
          # worker/local tests need Docker. Skip all in the Nix sandbox.
          checkFlags = [
            "-skip" "^TestValidateExternalURL"
          ];
          preCheck = ''
            rm -rf e2e
            rm -rf worker/local/*_test.go
          '';

          preBuild = ''
            rm -rf ui/dist
            cp -r ${ui} ui/dist
            chmod -R u+w ui/dist
          '';
        };
      };

      nixosModules.default = import ./nixos/module.nix self;

      devShells.${system}.default =
        let
          dev = pkgs.writeShellScriptBin "dev" ''
            sudo rm -rf ui/dist
            (cd ui && npm run build)
            exec sudo -E go run -mod=mod . serve -d /tmp/gamejanitor-data "$@"
          '';

          cli = pkgs.writeShellScriptBin "cli" ''
            exec go run . "$@"
          '';

          # TODO: migrate to ghcr.io/gamejanitor when going public
          build-image = pkgs.writeShellScriptBin "build-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: build-image <base|java8|java17|java21|java25|dotnet>"
              exit 1
            fi
            docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: push-image <base|java8|java17|java21|java25|dotnet>"
              exit 1
            fi
            echo "Building and pushing $image..."
            docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
            docker push "ghcr.io/warsmite/gamejanitor/$image"
          '';

          push-all-images = pkgs.writeShellScriptBin "push-all-images" ''
            # Build order matters: base must be built first since others depend on it
            for image in base java8 java17 java21 java25 dotnet; do
              echo "Building and pushing $image..."
              docker build -t "ghcr.io/warsmite/gamejanitor/$image" "images/$image"
              docker push "ghcr.io/warsmite/gamejanitor/$image"
            done
          '';

          # Multi-node dev — runs controller + worker as separate processes.
          # Both share the same machine but communicate via gRPC, catching
          # proto mismatches, registration bugs, and multi-node code paths.
          dev-multi = pkgs.writeShellScriptBin "dev-multi" ''
            set -e
            echo "Building UI..."
            sudo rm -rf ui/dist
            (cd ui && npm run build)

            CTRL_DIR=/tmp/gamejanitor-multi-controller
            WORK_DIR=/tmp/gamejanitor-multi-worker
            mkdir -p "$CTRL_DIR" "$WORK_DIR"

            cleanup() {
              echo "Stopping..."
              [ -n "''${CTRL_PID:-}" ] && kill "$CTRL_PID" 2>/dev/null || true
              [ -n "''${WORK_PID:-}" ] && kill "$WORK_PID" 2>/dev/null || true
              wait 2>/dev/null
            }
            trap cleanup EXIT

            echo "Starting controller on :8080 (gRPC :9090)..."
            go run -mod=mod . serve \
              --controller --worker=false \
              --port 8080 --grpc-port 9090 --sftp-port 0 \
              -d "$CTRL_DIR" &
            CTRL_PID=$!

            # Wait for controller gRPC to be ready
            for i in $(seq 1 30); do
              if nc -z localhost 9090 2>/dev/null; then break; fi
              sleep 0.2
            done

            echo "Starting worker (gRPC :9091, connecting to controller :9090)..."
            sudo -E go run -mod=mod . serve \
              --worker --controller=false \
              --grpc-port 9091 \
              --controller-address localhost:9090 \
              -d "$WORK_DIR" &
            WORK_PID=$!

            echo ""
            echo "Multi-node dev running:"
            echo "  Controller: http://localhost:8080 (gRPC :9090)"
            echo "  Worker:     gRPC :9091"
            echo "  Press Ctrl+C to stop both"
            echo ""

            wait
          '';

          # Individual process scripts for advanced use
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
            exec sudo -E go run . serve \
              --worker --controller=false \
              --grpc-port "$PORT" \
              --controller-address "$CTRL" \
              -d "/tmp/gamejanitor-worker-$PORT" "''${EXTRA_ARGS[@]}" "''${@:4}"
          '';

          update-vendor-hash = pkgs.writeShellScriptBin "update-vendor-hash" ''
            go mod vendor
            HASH=$(nix hash path --type sha256 vendor/)
            sed -i "s|vendorHash = \".*\"|vendorHash = \"$HASH\"|" flake.nix
            echo "Updated vendorHash to $HASH"
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
            echo "Running e2e tests (requires Docker + base image)..."
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

          loc = pkgs.writeShellScriptBin "loc" ''
            ${pkgs.tokei}/bin/tokei . \
              --exclude vendor --exclude node_modules --exclude 'worker/pb/*.go' \
              --types Go,TypeScript,TSX,CSS,HTML,Nix,YAML,Protobuf,SQL,Shell
          '';

          cleanup = pkgs.writeShellScriptBin "cleanup" ''
            if command -v docker &>/dev/null; then
              if docker info &>/dev/null; then
                echo "Cleaning up docker containers..."
                docker ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r docker rm -f
                echo "Cleaning up docker volumes..."
                docker volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r docker volume rm -f
              fi
              if sudo -n true 2>/dev/null && sudo docker info &>/dev/null; then
                echo "Cleaning up docker containers (rootful)..."
                sudo docker ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r sudo docker rm -f
                echo "Cleaning up docker volumes (rootful)..."
                sudo docker volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r sudo docker volume rm -f
              fi
            fi
            echo "Removing /tmp/gamejanitor-*..."
            rm -rf /tmp/gamejanitor-data /tmp/gamejanitor-controller /tmp/gamejanitor-worker-* /tmp/gamejanitor-multi-*
            echo "Cleanup complete."
          '';
        in
        pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.docker-client
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
            dev-multi
            dev-controller
            dev-worker
            gen-proto
            update-vendor-hash
            loc
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
