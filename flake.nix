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
        vendorHash = "sha256-ks0AFpolmErL2+pRTbHeoQ4dyav7HTSAZTOTiUzTj4Y=";
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
            reflex -s -r '\.(go|html)$' -- go run ./cmd/gamejanitor serve -d /tmp/gamejanitor-data "$@"
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
            docker build -t "registry.0xkowalski.dev/gamejanitor/$image" "images/$image"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            image="$1"
            if [ -z "$image" ]; then
              echo "Usage: push-image <base|steamcmd|java|dotnet>"
              exit 1
            fi
            echo "Building and pushing $image..."
            docker build -t "registry.0xkowalski.dev/gamejanitor/$image" "images/$image"
            docker push "registry.0xkowalski.dev/gamejanitor/$image"
          '';

          push-all-images = pkgs.writeShellScriptBin "push-all-images" ''
            # Build order matters: base must be built first since others depend on it
            for image in base steamcmd java dotnet; do
              echo "Building and pushing $image..."
              docker build -t "registry.0xkowalski.dev/gamejanitor/$image" "images/$image"
              docker push "registry.0xkowalski.dev/gamejanitor/$image"
            done
          '';

          cleanup = pkgs.writeShellScriptBin "cleanup" ''
            echo "Stopping and removing gamejanitor containers..."
            docker ps -a --filter "name=gamejanitor-" --format '{{.ID}}' | xargs -r docker rm -f
            echo "Removing gamejanitor volumes..."
            docker volume ls --filter "name=gamejanitor-" --format '{{.Name}}' | xargs -r docker volume rm -f
            echo "Removing /tmp/gamejanitor-data..."
            rm -rf /tmp/gamejanitor-data
            echo "Cleanup complete."
          '';
        in
        pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.docker-client
            pkgs.tailwindcss
            pkgs.reflex
            dev
            cli
            build-css
            build-image
            push-image
            push-all-images
            cleanup
          ];

          shellHook = ''
            export CGO_ENABLED=0
          '';
        };

    };
}
