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
      packages.${system} =
        let
          ui = pkgs.buildNpmPackage {
            pname = "gamejanitor-ui";
            version = "0.1.0";
            src = ./ui;
            npmDepsHash = "sha256-b09AEsgcy52kcGj7rMuriVJcSimjZRzxTB0BOSvqY+w=";
            installPhase = ''
              cp -r dist $out
            '';
          };
        in
        {
          default = pkgs.buildGoModule {
            pname = "gamejanitor";
            version = "0.1.0";
            src = ./.;
            vendorHash = "sha256-PxIqTrRYS7Vf6UfSfSNCj19sy162u49CBwZQjO4+ZSQ=";
            env.CGO_ENABLED = "0";

            # sdk/ and games/ are separate Go modules with their own go.mod — exclude from main build
            excludedPackages = [
              "sdk"
              "games"
            ];

            # e2e tests need a running instance; netutil DNS tests need network.
            checkFlags = [
              "-skip"
              "^TestValidateExternalURL"
            ];
            preCheck = ''
              rm -rf e2e
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
            update-vendor-hash
            rm -rf ui/dist 2>/dev/null || { chmod -R u+w ui/dist && rm -rf ui/dist; }
            (cd ui && npm run build)
            go run -mod=mod . serve -d /tmp/gamejanitor-data
          '';

          # Deploy to homelab — build binary + UI, ship to all nodes, restart services.
          # Usage: deploy (all nodes) | deploy sleepy (single node)
          # Deploy to homelab — build binary + UI, ship to all nodes, restart with dev binary.
          # Usage: deploy (all nodes) | deploy sleepy (single node)
          # Stops the NixOS service and runs the dev binary directly so we skip Nix rebuilds.
          # Use `deploy --restore` to go back to the NixOS-managed binary.
          deploy = pkgs.writeShellScriptBin "deploy" ''
                        set -e
                        NODES=("sleepy" "dopey" "grumpy")
                        FAST=false
                        if [ "$1" = "--fast" ]; then FAST=true; shift; fi
                        TARGETS=("''${@:-''${NODES[@]}}")

                        if [ "$FAST" = false ]; then
                          echo "Building UI..."
                          rm -rf ui/dist 2>/dev/null || rm -rf ui/dist 2>/dev/null || { chmod -R u+w ui/dist && rm -rf ui/dist; }
                          (cd ui && npm run build)
                        else
                          echo "Fast mode — skipping UI build"
                        fi

                        update-vendor-hash

                        echo "Building binary..."
                        CGO_ENABLED=0 go build -o /tmp/gamejanitor-deploy .
                        echo "Binary: $(du -h /tmp/gamejanitor-deploy | cut -f1)"

                        # Ship binary to all targets in parallel
                        for node in "''${TARGETS[@]}"; do
                          echo "Shipping binary to $node..."
                          ( scp /tmp/gamejanitor-deploy "$node:/tmp/gamejanitor-deploy" && \
                            ssh "$node" "sudo mv /tmp/gamejanitor-deploy /run/gamejanitor-dev && sudo chmod +x /run/gamejanitor-dev" && \
                            echo "  $node: deployed" ) &
                        done
                        wait

                        # Start controller first (sleepy), create worker tokens
                        CONTROLLER="sleepy"
                        WORKERS=("dopey" "grumpy")

                        # Write dev config with S3 backup store (Garage on homelab)
                        # These are local-only dev credentials, not real secrets.
                        ssh "$CONTROLLER" "sudo tee /var/lib/gamejanitor/dev-config.yaml > /dev/null" <<'YAML'
            backup_store:
              type: s3
              endpoint: "doc:3900"
              region: garage
              bucket: gamejanitor-backups
              path_style: true
              use_ssl: false
              access_key: "GKf4e7eacfd92f77f867981127"
              secret_key: "cb047f16267241dcf7be0836db30eaf747cdd66f7725815db98a2eb73eeb7303"
            YAML

                        echo "Starting controller ($CONTROLLER)..."
                        ssh "$CONTROLLER" "
                          sudo systemctl kill --signal=SIGKILL gamejanitor-dev 2>/dev/null || true
                          sudo systemctl stop gamejanitor-dev 2>/dev/null || true
                          sudo systemctl reset-failed gamejanitor-dev 2>/dev/null || true
                          sudo systemd-run --unit=gamejanitor-dev --property=Restart=always \
                            /run/gamejanitor-dev serve \
                              --config /var/lib/gamejanitor/dev-config.yaml \
                              --bind 0.0.0.0 --port 8080 --grpc-port 9090 --sftp-port 2222 \
                              --proxy \
                              -d /var/lib/gamejanitor
                        "
                        echo "  $CONTROLLER: started"

                        # Wait for controller DB to be ready
                        sleep 1

                        # Create all worker tokens in a single SSH call
                        echo "Creating worker tokens..."
                        TOKENDATA=$(ssh "$CONTROLLER" "
                          for w in ''${WORKERS[*]}; do
                            T=\$(sudo /run/gamejanitor-dev tokens offline create --name \"\$w\" --type worker -d /var/lib/gamejanitor 2>/dev/null || true)
                            [ -z \"\$T\" ] && T=\$(sudo /run/gamejanitor-dev tokens offline rotate --name \"\$w\" --type worker -d /var/lib/gamejanitor 2>/dev/null)
                            echo \"\$w=\$T\"
                          done
                        ")
                        declare -A TOKENS
                        while IFS='=' read -r name token; do
                          [ -n "$name" ] && [ -n "$token" ] && TOKENS[$name]="$token" && echo "  token: $name"
                        done <<< "$TOKENDATA"

                        # Start workers in parallel
                        for w in "''${WORKERS[@]}"; do
                          TOKEN="''${TOKENS[$w]}"
                          [ -z "$TOKEN" ] && continue
                          echo "Starting worker $w..."
                          ssh "$w" "
                            sudo systemctl kill --signal=SIGKILL gamejanitor-dev 2>/dev/null || true
                            sudo systemctl stop gamejanitor-dev 2>/dev/null || true
                            sudo systemctl reset-failed gamejanitor-dev 2>/dev/null || true
                            sudo systemd-run --unit=gamejanitor-dev --property=Restart=always \
                              /run/gamejanitor-dev serve \
                                --worker --controller=false \
                                --bind 0.0.0.0 --sftp-port 2222 \
                                --controller-address $CONTROLLER:9090 \
                                --worker-token '$TOKEN' \
                                -d /var/lib/gamejanitor
                          " &
                        done
                        wait
                        echo "All workers started"

                        echo "Deployed to: ''${TARGETS[*]}"
                        echo "Run 'deploy-restore' to switch back to NixOS-managed binary"
          '';

          deploy-restore = pkgs.writeShellScriptBin "deploy-restore" ''
            NODES=("sleepy" "dopey" "grumpy")
            TARGETS=("''${@:-''${NODES[@]}}")
            for node in "''${TARGETS[@]}"; do
              echo "Restoring $node to NixOS service..."
              ssh "$node" "sudo systemctl kill gamejanitor-dev 2>/dev/null; sudo rm -f /run/gamejanitor-dev; sudo systemctl start gamejanitor" || true
              echo "  $node: restored"
            done
          '';

          # Wipe everything on homelab nodes — DB, sandbox instances, volumes, data dir.
          # Usage: deploy-clean (all nodes) | deploy-clean sleepy
          deploy-clean = pkgs.writeShellScriptBin "deploy-clean" ''
            set -e
            NODES=("sleepy" "dopey" "grumpy")
            TARGETS=("''${@:-''${NODES[@]}}")

            echo "This will DELETE all gamejanitor data on: ''${TARGETS[*]}"
            echo "  - Database"
            echo "  - Sandbox instances and volumes"
            echo "  - Backups, game data, everything"
            read -p "Are you sure? (y/N) " -n 1 -r
            echo
            [[ ! $REPLY =~ ^[Yy]$ ]] && exit 1

            for node in "''${TARGETS[@]}"; do
              echo "Cleaning $node..."
              ssh "$node" "
                sudo systemctl kill --signal=SIGKILL gamejanitor-dev 2>/dev/null || true
                sudo systemctl stop gamejanitor-dev 2>/dev/null || true
                sudo systemctl reset-failed gamejanitor-dev 2>/dev/null || true
                for scope in \$(sudo systemctl list-units --type=scope --no-legend 'gj-gamejanitor-*' | awk '{print \$1}'); do
                  sudo systemctl kill --signal=SIGKILL \"\$scope\" 2>/dev/null || true
                  sudo systemctl stop \"\$scope\" 2>/dev/null || true
                done
                grep -o '/var/lib/gamejanitor/[^ ]*' /proc/mounts | sort -r | xargs -r -n1 sudo umount 2>/dev/null || true
                sudo rm -rf /var/lib/gamejanitor/*
                sudo rm -f /run/gamejanitor-dev
              "
              echo "  $node: clean"
            done

            # Wipe S3 backup bucket (Garage on homelab — local dev credentials, not real secrets)
            echo "Cleaning S3 backup bucket..."
            AWS_ACCESS_KEY_ID=GKf4e7eacfd92f77f867981127 \
            AWS_SECRET_ACCESS_KEY=cb047f16267241dcf7be0836db30eaf747cdd66f7725815db98a2eb73eeb7303 \
            ${pkgs.awscli2}/bin/aws --endpoint-url http://doc:3900 --region garage \
              s3 rm s3://gamejanitor-backups/ --recursive 2>/dev/null || true
            echo "  s3: clean"

            echo "All clean. Run 'deploy' to start fresh."
          '';

          cli = pkgs.writeShellScriptBin "cli" ''
            exec go run . "$@"
          '';

          build-image = pkgs.writeShellScriptBin "build-image" ''
            docker build -t "ghcr.io/warsmite/gamejanitor/runtime" "oci/"
          '';

          push-image = pkgs.writeShellScriptBin "push-image" ''
            echo "Building and pushing runtime image..."
            docker build -t "ghcr.io/warsmite/gamejanitor/runtime" "oci/"
            docker push "ghcr.io/warsmite/gamejanitor/runtime"
          '';

          # Multi-node dev — runs controller + worker as separate processes.
          # Both share the same machine but communicate via gRPC, catching
          # proto mismatches, registration bugs, and multi-node code paths.
          dev-multi = pkgs.writeShellScriptBin "dev-multi" ''
            set -e
            update-vendor-hash
            echo "Building UI..."
            rm -rf ui/dist 2>/dev/null || { chmod -R u+w ui/dist && rm -rf ui/dist; }
            (cd ui && npm run build)

            CTRL_DIR=/tmp/gamejanitor-multi-controller
            WORK_DIR=/tmp/gamejanitor-multi-worker
            mkdir -p "$CTRL_DIR" "$WORK_DIR"

            # Create worker token via offline DB access (idempotent — reuses existing)
            WORKER_TOKEN=$(go run -mod=mod . tokens offline create \
              --name dev-worker --type worker -d "$CTRL_DIR" 2>/dev/null || true)
            if [ -z "$WORKER_TOKEN" ]; then
              # Token already exists — rotate to get a fresh secret
              WORKER_TOKEN=$(go run -mod=mod . tokens offline rotate \
                --name dev-worker --type worker -d "$CTRL_DIR" 2>/dev/null)
            fi

            cleanup() {
              echo "Stopping..."
              [ -n "''${CTRL_PID:-}" ] && kill "$CTRL_PID" 2>/dev/null || true
              [ -n "''${WORK_PID:-}" ] && sudo kill "$WORK_PID" 2>/dev/null || true
              [ -n "''${WORK2_PID:-}" ] && sudo kill "$WORK2_PID" 2>/dev/null || true
              wait 2>/dev/null
            }
            trap cleanup EXIT

            echo "Starting controller on :8080 (gRPC :9090, no SFTP)..."
            go run -mod=mod . serve \
              --controller --worker=false \
              --port 8080 --grpc-port 9090 --sftp-port 0 \
              -d "$CTRL_DIR" &
            CTRL_PID=$!

            # Wait for controller gRPC to be fully ready (not just port open)
            echo "Waiting for controller gRPC..."
            for i in $(seq 1 60); do
              if ${pkgs.netcat-openbsd}/bin/nc -z localhost 9090 2>/dev/null; then
                sleep 1  # extra settle time for gRPC service init
                break
              fi
              sleep 0.5
            done

            echo "Starting worker-1 (gRPC :9091, SFTP :2222, connecting to controller :9090)..."
            sudo -E go run -mod=mod . serve \
              --worker --controller=false \
              --bind 0.0.0.0 \
              --grpc-port 9091 --sftp-port 2222 \
              --worker-id worker-1 \
              --controller-address localhost:9090 \
              --worker-token "$WORKER_TOKEN" \
              -d "$WORK_DIR" &
            WORK_PID=$!

            WORK2_DIR=/tmp/gamejanitor-multi-worker-2
            mkdir -p "$WORK2_DIR"

            # Create second worker token
            WORKER2_TOKEN=$(go run -mod=mod . tokens offline create \
              --name dev-worker-2 --type worker -d "$CTRL_DIR" 2>/dev/null || true)
            if [ -z "$WORKER2_TOKEN" ]; then
              WORKER2_TOKEN=$(go run -mod=mod . tokens offline rotate \
                --name dev-worker-2 --type worker -d "$CTRL_DIR" 2>/dev/null)
            fi

            echo "Starting worker-2 (gRPC :9092, SFTP :2223, connecting to controller :9090)..."
            sudo -E go run -mod=mod . serve \
              --worker --controller=false \
              --bind 0.0.0.0 \
              --grpc-port 9092 --sftp-port 2223 \
              --worker-id worker-2 \
              --controller-address localhost:9090 \
              --worker-token "$WORKER2_TOKEN" \
              -d "$WORK2_DIR" &
            WORK2_PID=$!

            echo ""
            echo "Multi-node dev running:"
            echo "  Controller: http://localhost:8080 (gRPC :9090)"
            echo "  Worker 1:   gRPC :9091, SFTP :2222"
            echo "  Worker 2:   gRPC :9092, SFTP :2223"
            echo "  Press Ctrl+C to stop all"
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

          # Sync vendor/ with go.mod and keep flake.nix's vendorHash matching vendor/.
          # Silent no-op when nothing changed — safe to prepend to dev/build/test scripts
          # so the user never has to remember this step after editing games/ or sdk/.
          update-vendor-hash = pkgs.writeShellScriptBin "update-vendor-hash" ''
            go mod vendor
            HASH=$(nix hash path --type sha256 vendor/)
            CURRENT=$(grep -oP '(?<=vendorHash = "sha256-PxIqTrRYS7Vf6UfSfSNCj19sy162u49CBwZQjO4+ZSQ="]+' flake.nix | head -1)
            if [ "$HASH" != "$CURRENT" ]; then
              sed -i "s|vendorHash = \".*\"|vendorHash = \"$HASH\"|" flake.nix
              echo "Updated vendorHash: $CURRENT -> $HASH"
            fi
          '';

          # Read-only sibling for CI: fails if vendor/ or vendorHash is out of sync.
          # Runs `go mod vendor` (mutating), then verifies the working tree is clean.
          vendor-check = pkgs.writeShellScriptBin "vendor-check" ''
            set -e
            go mod vendor
            HASH=$(nix hash path --type sha256 vendor/)
            CURRENT=$(grep -oP '(?<=vendorHash = "sha256-PxIqTrRYS7Vf6UfSfSNCj19sy162u49CBwZQjO4+ZSQ="]+' flake.nix | head -1)
            if [ "$HASH" != "$CURRENT" ]; then
              echo "vendor-check: vendorHash out of sync."
              echo "  flake.nix: $CURRENT"
              echo "  vendor/:   $HASH"
              echo "Fix with: update-vendor-hash"
              exit 1
            fi
            if ! git diff --quiet vendor/ 2>/dev/null; then
              echo "vendor-check: vendor/ out of sync with go.mod. Fix with: update-vendor-hash"
              git diff --stat vendor/ | head -10
              exit 1
            fi
          '';

          gen-proto = pkgs.writeShellScriptBin "gen-proto" ''
            protoc --go_out=. --go_opt=module=github.com/warsmite/gamejanitor \
                   --go-grpc_out=. --go-grpc_opt=module=github.com/warsmite/gamejanitor \
                   worker/proto/worker.proto
          '';

          build = pkgs.writeShellScriptBin "build" ''
            set -e
            update-vendor-hash
            echo "Building UI..."
            cd ui && npm run build && cd ..
            echo "Building Go binary..."
            go clean -cache
            go build -o gamejanitor .
            echo "Done: ./gamejanitor"
          '';

          test-all = pkgs.writeShellScriptBin "test-all" ''
            update-vendor-hash
            exec go test -tags integration ./... "$@"
          '';

          test-race = pkgs.writeShellScriptBin "test-race" ''
            update-vendor-hash
            exec CGO_ENABLED=1 go test -race ./... "$@"
          '';

          test-e2e = pkgs.writeShellScriptBin "test-e2e" ''
            update-vendor-hash
            echo "Building gamejanitor..."
            go build -o /tmp/gamejanitor-e2e . || exit 1
            echo "Running e2e tests..."
            exec go test -tags e2e -count=1 -timeout "''${TEST_TIMEOUT:-5m}" -v ./e2e/ "$@"
          '';

          test-games = pkgs.writeShellScriptBin "test-games" ''
            update-vendor-hash
            if [ -n "$1" ] && [[ "$1" != -* ]]; then
              export E2E_GAMES="$1"
              shift
            fi
            echo "Building gamejanitor..."
            go build -o /tmp/gamejanitor-e2e .
            echo "Running game compatibility tests (E2E_GAMES=''${E2E_GAMES:-minecraft-java})..."
            exec go test -tags e2e -run TestGame_Compatibility -timeout "''${TEST_TIMEOUT:-15m}" -v ./e2e/ "$@"
          '';

          test-coverage = pkgs.writeShellScriptBin "test-coverage" ''
            set -e
            update-vendor-hash
            go test -coverprofile=/tmp/gamejanitor-coverage.out ./... "$@"
            echo ""
            echo "=== Per-package coverage ==="
            go tool cover -func=/tmp/gamejanitor-coverage.out | grep "^total:"
            echo ""
            echo "=== Per-package breakdown ==="
            go test -cover ./... 2>&1 | grep "coverage:"
            echo ""
            echo "HTML report: go tool cover -html=/tmp/gamejanitor-coverage.out"
          '';

          # Run e2e against homelab. Same tests, pointed at the cluster.
          # Usage: test-homelab | E2E_GAME_ID=test-game test-homelab
          test-homelab = pkgs.writeShellScriptBin "test-homelab" ''
            echo "Cleaning and deploying to homelab..."
            deploy-clean
            deploy
            echo "Waiting for workers to connect..."
            sleep 3

            # Capture logs from all nodes for post-mortem debugging
            LOG_DIR="/tmp/gamejanitor-e2e-logs"
            rm -rf "$LOG_DIR"
            mkdir -p "$LOG_DIR"
            ssh sleepy 'journalctl -u gamejanitor-dev -f --no-pager -o cat' > "$LOG_DIR/sleepy.log" 2>&1 &
            PID_SLEEPY=$!
            ssh dopey 'journalctl -u gamejanitor-dev -f --no-pager -o cat' > "$LOG_DIR/dopey.log" 2>&1 &
            PID_DOPEY=$!
            ssh grumpy 'journalctl -u gamejanitor-dev -f --no-pager -o cat' > "$LOG_DIR/grumpy.log" 2>&1 &
            PID_GRUMPY=$!

            export GAMEJANITOR_API_URL="http://sleepy:8080"
            export E2E_GAME_ID="''${E2E_GAME_ID:-minecraft-java}"
            echo "Running e2e against homelab (game=$E2E_GAME_ID)..."
            echo "Logs: $LOG_DIR/{sleepy,dopey,grumpy}.log"
            go test -tags e2e -count=1 -timeout "''${TEST_TIMEOUT:-10m}" -parallel "''${TEST_PARALLEL:-3}" -v ./e2e/ "$@" 2>&1 | tee "$LOG_DIR/test-output.log"
            EXIT=''${PIPESTATUS[0]}

            # Dump events via API — structured audit log of everything that happened
            curl -s "http://sleepy:8080/api/events/history?limit=500" > "$LOG_DIR/events.json" 2>&1 || true

            # Snapshot cluster state after tests (raw JSON — grep/jq as needed)
            curl -s http://sleepy:8080/api/workers > "$LOG_DIR/workers.json" 2>&1 || true
            curl -s http://sleepy:8080/api/gameservers > "$LOG_DIR/gameservers.json" 2>&1 || true

            kill $PID_SLEEPY $PID_DOPEY $PID_GRUMPY 2>/dev/null
            wait $PID_SLEEPY $PID_DOPEY $PID_GRUMPY 2>/dev/null

            echo ""
            echo "=== E2E LOGS: $LOG_DIR ==="
            ls -1 "$LOG_DIR"/ | sed 's/^/  /'
            if [ $EXIT -ne 0 ]; then
              echo ""
              echo "Debug:"
              echo "  grep '<gs-id>' $LOG_DIR/*.log"
              echo "  grep 'error\|WARN\|unexpected' $LOG_DIR/*.log"
              echo "  cat $LOG_DIR/events.log | grep '<gs-id>'"
            fi
            exit $EXIT
          '';

          loc = pkgs.writeShellScriptBin "loc" ''
            ${pkgs.tokei}/bin/tokei . \
              --exclude vendor --exclude node_modules --exclude 'worker/proto/*.pb.go' \
              --types Go,TypeScript,TSX,CSS,HTML,Nix,YAML,Protobuf,SQL,Shell
          '';

          cleanup = pkgs.writeShellScriptBin "cleanup" ''
            echo "Removing /tmp/gamejanitor-*..."
            sudo rm -rf /tmp/gamejanitor-data /tmp/gamejanitor-controller /tmp/gamejanitor-worker-* /tmp/gamejanitor-multi-*
            echo "Cleanup complete."
          '';
        in
        pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
            pkgs.nodejs
            dev
            deploy
            deploy-restore
            deploy-clean
            cli
            build
            build-image
            push-image
            dev-multi
            dev-controller
            dev-worker
            gen-proto
            update-vendor-hash
            vendor-check
            loc
            cleanup
            test-all
            test-race
            test-e2e
            test-homelab
            test-games
            test-coverage
          ];

          shellHook = ''
            export CGO_ENABLED=0
          '';
        };

    };
}
