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

          build-image = pkgs.writeShellScriptBin "build-image" ''
            game="$1"
            if [ -z "$game" ]; then
              echo "Usage: build-image <game>"
              exit 1
            fi
            docker build -t "registry.0xkowalski.dev/gamejanitor/$game" "images/$game"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            game="$1"
            if [ -z "$game" ]; then
              echo "Usage: push-image <game>"
              exit 1
            fi
            echo "Building and pushing $game..."
            docker build -t "registry.0xkowalski.dev/gamejanitor/$game" "images/$game"
            docker push "registry.0xkowalski.dev/gamejanitor/$game"
          '';

          push-all-images = pkgs.writeShellScriptBin "push-all-images" ''
            for dir in images/*/; do
              game=$(basename "$dir")
              echo "Building and pushing $game..."
              docker build -t "registry.0xkowalski.dev/gamejanitor/$game" "images/$game"
              docker push "registry.0xkowalski.dev/gamejanitor/$game"
            done
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
          ];

          shellHook = ''
            export CGO_ENABLED=0
          '';
        };

    };
}
