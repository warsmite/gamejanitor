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
      packages.${system}.default = pkgs.buildGoModule {
        pname = "gamejanitor";
        version = "0.1.0";
        src = ./.;
        vendorHash = "sha256-gQgVaBuFxSlH3pGDmmKJ/K88Xe40W608yCc/3BiKh5g=";
        env.CGO_ENABLED = "0";
        nativeBuildInputs = [ pkgs.tailwindcss ];
        preBuild = ''
          tailwindcss -c ./tailwind.config.js --content "./internal/web/templates/**/*.html" -i internal/web/static/input.css -o internal/web/static/style.css --minify
        '';
        subPackages = [ "cmd/gamejanitor" ];
      };

      nixosModules.default = import ./nixos/module.nix self;

      devShells.${system}.default =
        let
          dev = pkgs.writeShellScriptBin "dev" ''
            # Build CSS initially
            tailwindcss -c ./tailwind.config.js --content "./internal/web/templates/**/*.html" -i internal/web/static/input.css -o internal/web/static/style.css --minify

            # Watch templates for CSS rebuild in background
            reflex -s -r '\.html$' -- tailwindcss -c ./tailwind.config.js --content "./internal/web/templates/**/*.html" -i internal/web/static/input.css -o internal/web/static/style.css --minify &

            # Watch Go/template files and restart server
            reflex -s -r '\.(go|html)$' -r 'internal/games/data' -- go run ./cmd/gamejanitor serve -d /tmp/gamejanitor-data "$@"
          '';

          cli = pkgs.writeShellScriptBin "cli" ''
            exec go run ./cmd/gamejanitor "$@"
          '';

          build-css = pkgs.writeShellScriptBin "build-css" ''
            tailwindcss -c ./tailwind.config.js --content "./internal/web/templates/**/*.html" -i internal/web/static/input.css -o internal/web/static/style.css --minify
          '';

          # TODO: migrate to ghcr.io/gamejanitor when going public
          build-image = pkgs.writeShellScriptBin "build-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: build-image <base|steamcmd|java|dotnet>"
              exit 1
            fi
            docker build -t "ghcr.io/0xkowalskidev/gamejanitor/$image" "images/$image"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: push-image <base|steamcmd|java|dotnet>"
              exit 1
            fi
            echo "Building and pushing $image..."
            docker build -t "ghcr.io/0xkowalskidev/gamejanitor/$image" "images/$image"
            docker push "ghcr.io/0xkowalskidev/gamejanitor/$image"
          '';

          push-all-images = pkgs.writeShellScriptBin "push-all-images" ''
            # Build order matters: base must be built first since others depend on it
            for image in base steamcmd java dotnet; do
              echo "Building and pushing $image..."
              docker build -t "ghcr.io/0xkowalskidev/gamejanitor/$image" "images/$image"
              docker push "ghcr.io/0xkowalskidev/gamejanitor/$image"
            done
          '';

          # Multi-node test scripts — separate data dirs so they don't conflict
          dev-controller = pkgs.writeShellScriptBin "dev-controller" ''
            echo "Starting controller on :8090 (gRPC :9090)"
            exec go run ./cmd/gamejanitor serve \
              --role controller+worker \
              -p 8090 --grpc-port 9090 --sftp-port 2022 \
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
            exec go run ./cmd/gamejanitor serve \
              --role worker \
              --grpc-port "$PORT" \
              --controller "$CTRL" \
              -d "/tmp/gamejanitor-worker-$PORT" "''${EXTRA_ARGS[@]}" "''${@:4}"
          '';

          gen-proto = pkgs.writeShellScriptBin "gen-proto" ''
            protoc --go_out=. --go_opt=module=github.com/0xkowalskidev/gamejanitor \
                   --go-grpc_out=. --go-grpc_opt=module=github.com/0xkowalskidev/gamejanitor \
                   proto/worker.proto
          '';

          cleanup = pkgs.writeShellScriptBin "cleanup" ''
            echo "Stopping and removing gamejanitor containers..."
            docker ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r docker rm -f
            echo "Removing gamejanitor volumes..."
            docker volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r docker volume rm -f
            echo "Removing /tmp/gamejanitor-*..."
            rm -rf /tmp/gamejanitor-data /tmp/gamejanitor-controller /tmp/gamejanitor-worker-*
            echo "Cleanup complete."
          '';
        in
        pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.docker-client
            pkgs.tailwindcss
            pkgs.reflex
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
            dev
            cli
            build-css
            build-image
            push-image
            push-all-images
            dev-controller
            dev-worker
            gen-proto
            cleanup
          ];

          shellHook = ''
            export CGO_ENABLED=0
          '';
        };

    };
}
