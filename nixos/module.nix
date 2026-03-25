self:

{ config, lib, pkgs, ... }:

let
  cfg = config.services.gamejanitor;
  hasLocalWorker = cfg.worker;
  isWorkerOnly = cfg.worker && !cfg.controller;

  # Build the YAML config file from NixOS options
  configFile = pkgs.writeText "gamejanitor.yaml" (builtins.toJSON (
    {
      bind = cfg.bindAddress;
      port = cfg.port;
      controller = cfg.controller;
      worker = cfg.worker;
      data_dir = cfg.dataDir;
      web_ui = cfg.webUI;
    }
    // lib.optionalAttrs (cfg.mode != "") { mode = cfg.mode; }
    // lib.optionalAttrs (cfg.containerRuntime != "auto") { container_runtime = cfg.containerRuntime; }
    // lib.optionalAttrs (cfg.containerSocket != null) { container_socket = cfg.containerSocket; }
    // lib.optionalAttrs (cfg.grpcPort != null) { grpc_port = cfg.grpcPort; }
    // lib.optionalAttrs (cfg.workerGrpcPort != null) { worker_grpc_port = cfg.workerGrpcPort; }
    // lib.optionalAttrs (cfg.sftpPort != null) { sftp_port = cfg.sftpPort; }
    // lib.optionalAttrs (cfg.controllerAddress != null) { controller_address = cfg.controllerAddress; }
    // lib.optionalAttrs (cfg.workerId != null) { worker_id = cfg.workerId; }
    // lib.optionalAttrs (cfg.workerLimits != {}) {
      worker_limits = lib.filterAttrs (_: v: v != null) {
        max_memory_mb = cfg.workerLimits.maxMemoryMB;
        max_cpu = cfg.workerLimits.maxCPU;
        max_storage_mb = cfg.workerLimits.maxStorageMB;
      };
    }
    // lib.optionalAttrs (cfg.tls.ca != null) {
      tls = {
        ca = cfg.tls.ca;
        cert = cfg.tls.cert;
        key = cfg.tls.key;
      };
    }
    // lib.optionalAttrs (cfg.backupStore.type != "local") {
      backup_store = lib.filterAttrs (_: v: v != null && v != "") {
        type = cfg.backupStore.type;
        endpoint = cfg.backupStore.endpoint;
        bucket = cfg.backupStore.bucket;
        region = cfg.backupStore.region;
        path_style = cfg.backupStore.pathStyle;
        use_ssl = cfg.backupStore.useSSL;
      };
    }
    // lib.optionalAttrs (cfg.settings != {}) { settings = cfg.settings; }
  ));
in {
  options.services.gamejanitor = {
    enable = lib.mkEnableOption "Gamejanitor game server manager";

    mode = lib.mkOption {
      type = lib.types.enum [ "" "business" ];
      default = "";
      description = ''
        Settings profile. "business" enables auth, rate limiting,
        and resource limit requirements by default.
      '';
    };

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "The gamejanitor package to use.";
    };

    controller = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the controller role (API server, orchestrator).";
    };

    worker = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the worker role (Docker container management).";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 8080;
      description = "Port for the API server.";
    };

    bindAddress = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1";
      description = "Bind address for all listeners (HTTP, SFTP, gRPC).";
    };

    dataDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/gamejanitor";
      description = "Directory for database, backups, and game data.";
    };

    grpcPort = lib.mkOption {
      type = lib.types.nullOr lib.types.port;
      default = null;
      description = "gRPC port for worker communication. Required for multi-node.";
    };

    workerGrpcPort = lib.mkOption {
      type = lib.types.nullOr lib.types.port;
      default = null;
      description = "Worker gRPC port for dial-back. Used in controller+worker mode.";
    };

    sftpPort = lib.mkOption {
      type = lib.types.nullOr lib.types.port;
      default = null;
      description = "SFTP server port for file access. null to disable.";
    };

    controllerAddress = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "192.168.1.10:9090";
      description = "Controller gRPC address. Required for worker-only nodes.";
    };

    workerId = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Worker ID. Defaults to hostname.";
    };

    workerTokenFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        Path to a file containing the worker auth token.
        Works with sops-nix, agenix, or any secret manager.
      '';
    };

    workerLimits = {
      maxMemoryMB = lib.mkOption {
        type = lib.types.nullOr lib.types.int;
        default = null;
        description = "Total memory available for gameservers on this worker (MB).";
      };

      maxCPU = lib.mkOption {
        type = lib.types.nullOr lib.types.float;
        default = null;
        description = "Total CPU cores available for gameservers on this worker.";
      };

      maxStorageMB = lib.mkOption {
        type = lib.types.nullOr lib.types.int;
        default = null;
        description = "Total storage available for gameservers on this worker (MB).";
      };

    };

    tls = {
      ca = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to CA certificate for gRPC mTLS.";
      };

      cert = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to certificate for gRPC mTLS.";
      };

      key = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to private key for gRPC mTLS.";
      };
    };

    backupStore = {
      type = lib.mkOption {
        type = lib.types.enum [ "local" "s3" ];
        default = "local";
        description = "Backup storage backend.";
      };

      endpoint = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "S3-compatible endpoint.";
      };

      bucket = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "S3 bucket name.";
      };

      region = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "S3 region.";
      };

      accessKeyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to file containing backup store access key.";
      };

      secretKeyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to file containing backup store secret key.";
      };

      pathStyle = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Use path-style URLs (required for MinIO).";
      };

      useSSL = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Use HTTPS for S3 connections.";
      };
    };

    # Runtime settings — written to DB on every startup via config file settings block.
    # This is the declarative convergence model: config file is source of truth,
    # API changes are temporary runtime overrides that reset on restart.
    settings = lib.mkOption {
      type = lib.types.attrsOf (lib.types.oneOf [ lib.types.bool lib.types.int lib.types.str ]);
      default = {};
      example = {
        auth_enabled = true;
        localhost_bypass = false;
        max_backups = 5;
        require_memory_limit = true;
      };
      description = ''
        Runtime settings written to the DB on every startup.
        See CONFIG_SPEC.md for available keys. API/UI changes are
        temporary — they reset to these values on restart.
      '';
    };

    webUI = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the embedded web UI. Disable for API-only deployments.";
    };

    containerRuntime = lib.mkOption {
      type = lib.types.enum [ "auto" "docker" "podman" "process" ];
      default = "auto";
      description = "Container runtime. Auto-detects Podman then Docker by default.";
    };

    containerSocket = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Explicit container runtime socket path. Auto-detected if null.";
    };

    environment = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {};
      description = "Extra environment variables (e.g. DEBUG=1).";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open service ports and the gameserver port range in the firewall.";
    };
  };

  config = lib.mkIf cfg.enable (let
    portRangeStart = cfg.settings.port_range_start or 27000;
    portRangeEnd = cfg.settings.port_range_end or 28999;
  in {
    assertions = [
      {
        assertion = isWorkerOnly -> cfg.controllerAddress != null;
        message = "services.gamejanitor.controllerAddress must be set for worker-only nodes.";
      }
      {
        assertion = isWorkerOnly -> cfg.workerTokenFile != null;
        message = "services.gamejanitor.workerTokenFile must be set for worker-only nodes.";
      }
      {
        assertion = (cfg.controller && cfg.worker && cfg.controllerAddress == null) || cfg.grpcPort != null || (!cfg.controller || !cfg.worker);
        message = "services.gamejanitor.grpcPort must be set for multi-node deployments.";
      }
    ];

    virtualisation.docker.enable = lib.mkIf (hasLocalWorker && cfg.containerRuntime != "podman" && cfg.containerRuntime != "process") true;

    systemd.services.gamejanitor = {
      description = "Gamejanitor Game Server Manager";
      after = [ "network-online.target" ] ++ lib.optional hasLocalWorker "docker.service";
      wants = [ "network-online.target" ] ++ lib.optional hasLocalWorker "docker.service";
      wantedBy = [ "multi-user.target" ];

      environment = cfg.environment;

      script = ''
        ${lib.optionalString (cfg.workerTokenFile != null) ''
          export GJ_WORKER_TOKEN="$(cat ${lib.escapeShellArg cfg.workerTokenFile})"
        ''}
        ${lib.optionalString (cfg.backupStore.accessKeyFile != null) ''
          export GJ_BACKUP_STORE_ACCESS_KEY="$(cat ${lib.escapeShellArg cfg.backupStore.accessKeyFile})"
        ''}
        ${lib.optionalString (cfg.backupStore.secretKeyFile != null) ''
          export GJ_BACKUP_STORE_SECRET_KEY="$(cat ${lib.escapeShellArg cfg.backupStore.secretKeyFile})"
        ''}
        exec ${cfg.package}/bin/gamejanitor serve --config ${configFile}
      '';

      serviceConfig = {
        Type = "simple";
        Restart = "always";
        RestartSec = 5;

        SupplementaryGroups = lib.optional hasLocalWorker "docker";

        DynamicUser = true;
        StateDirectory = "gamejanitor";
      };
    };

    networking.firewall = lib.mkIf cfg.openFirewall {
      allowedTCPPorts =
        lib.optional cfg.controller cfg.port
        ++ lib.optional (cfg.grpcPort != null) cfg.grpcPort
        ++ lib.optional (cfg.sftpPort != null) cfg.sftpPort
        ++ lib.optional (cfg.workerGrpcPort != null) cfg.workerGrpcPort;
      allowedTCPPortRanges = lib.optional hasLocalWorker { from = portRangeStart; to = portRangeEnd; };
      allowedUDPPortRanges = lib.optional hasLocalWorker { from = portRangeStart; to = portRangeEnd; };
    };
  });
}
