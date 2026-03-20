self:

{ config, lib, pkgs, ... }:

let
  cfg = config.services.gamejanitor;
  hasLocalWorker = cfg.role == "standalone" || cfg.role == "worker" || cfg.role == "controller+worker";
  hasWebUI = cfg.role != "worker";
in {
  options.services.gamejanitor = {
    enable = lib.mkEnableOption "Gamejanitor game server manager";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "The gamejanitor package to use.";
    };

    role = lib.mkOption {
      type = lib.types.enum [ "standalone" "controller" "worker" "controller+worker" ];
      default = "standalone";
      description = ''
        Deployment role for this node.
        - standalone: single node, runs everything (default)
        - controller: multi-node controller with web UI, no local Docker
        - worker: headless worker agent, connects to a controller
        - controller+worker: controller with web UI and local Docker
      '';
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 8080;
      description = "Port for the web UI and API. Ignored in worker role.";
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
      description = ''
        gRPC port for worker communication. Required for multi-node roles.
        Set to null to disable (standalone mode).
      '';
    };

    sftpPort = lib.mkOption {
      type = lib.types.nullOr lib.types.port;
      default = null;
      description = ''
        SFTP server port for file access. Set to null to disable.
        Currently only runs on controller/standalone nodes — file operations
        for remote workers are proxied over gRPC through the controller.
      '';
    };

    controller = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "192.168.1.10:9090";
      description = "Controller gRPC address for worker registration. Required for worker role.";
    };

    workerId = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Worker ID. Defaults to the machine hostname if not set.";
    };

    workerTokenFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        Path to a file containing the worker auth token (raw token, no KEY=VALUE wrapper).
        Works with sops-nix, agenix, or any secret manager that writes a plaintext file.
        Required for worker role.
      '';
    };

    connectionAddress = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "game.example.com";
      description = ''
        Public address shown in gameserver connection info.
        Overrides per-worker IP detection. Set this if all nodes share
        a single external address (e.g. reverse proxy, VPN endpoint).
      '';
    };

    auth = {
      enable = lib.mkOption {
        type = lib.types.nullOr lib.types.bool;
        default = null;
        description = "Enable token-based authentication. null = leave at DB/default (off).";
      };

      localhostBypass = lib.mkOption {
        type = lib.types.nullOr lib.types.bool;
        default = null;
        description = "Allow unauthenticated requests from localhost. null = leave at DB/default (on).";
      };
    };

    portRange = {
      start = lib.mkOption {
        type = lib.types.nullOr lib.types.port;
        default = null;
        description = "Start of the gameserver port allocation range (default: 27000).";
      };

      end = lib.mkOption {
        type = lib.types.nullOr lib.types.port;
        default = null;
        description = "End of the gameserver port allocation range (default: 28999).";
      };
    };

    portMode = lib.mkOption {
      type = lib.types.nullOr (lib.types.enum [ "auto" "manual" ]);
      default = null;
      description = "Port allocation mode. null = leave at DB/default (auto).";
    };

    maxBackups = lib.mkOption {
      type = lib.types.nullOr lib.types.int;
      default = null;
      description = "Max backups per gameserver. 0 = unlimited. null = leave at DB/default (10).";
    };

    s3 = {
      bucket = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "S3 bucket name for backup storage. Enables S3 backend when set.";
      };

      endpoint = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "s3.amazonaws.com";
        description = "S3-compatible endpoint (e.g. s3.amazonaws.com, minio.local:9000).";
      };

      region = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "S3 region (e.g. us-east-1). Defaults to us-east-1 if not set.";
      };

      accessKeyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to file containing S3 access key. Works with sops-nix, agenix, etc.";
      };

      secretKeyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to file containing S3 secret key. Works with sops-nix, agenix, etc.";
      };

      pathStyle = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Use path-style addressing. Required for MinIO and most NAS S3 implementations.";
      };

      useSSL = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Use HTTPS for S3 connections. Set false for local HTTP endpoints.";
      };
    };

    grpcTLS = {
      caFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to CA certificate for gRPC mTLS. Required on worker nodes.";
      };

      certFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to client/server certificate for gRPC mTLS. Required on worker nodes.";
      };

      keyFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        description = "Path to private key for gRPC mTLS. Required on worker nodes.";
      };
    };

    environment = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {};
      description = ''
        Extra environment variables passed to the gamejanitor service.
        Settings options above are preferred — use this for variables
        not covered by dedicated options (e.g. DEBUG=1).
      '';
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open service ports (web UI, gRPC, SFTP) and the gameserver port range in the firewall.";
    };
  };

  config = lib.mkIf cfg.enable (let
    portRangeStart = if cfg.portRange.start != null then cfg.portRange.start else 27000;
    portRangeEnd = if cfg.portRange.end != null then cfg.portRange.end else 28999;

    settingsEnv =
      lib.optionalAttrs (cfg.connectionAddress != null) { GJ_CONNECTION_ADDRESS = cfg.connectionAddress; }
      // lib.optionalAttrs (cfg.auth.enable != null) { GJ_AUTH_ENABLED = lib.boolToString cfg.auth.enable; }
      // lib.optionalAttrs (cfg.auth.localhostBypass != null) { GJ_LOCALHOST_BYPASS = lib.boolToString cfg.auth.localhostBypass; }
      // lib.optionalAttrs (cfg.portRange.start != null) { GJ_PORT_RANGE_START = toString cfg.portRange.start; }
      // lib.optionalAttrs (cfg.portRange.end != null) { GJ_PORT_RANGE_END = toString cfg.portRange.end; }
      // lib.optionalAttrs (cfg.portMode != null) { GJ_PORT_MODE = cfg.portMode; }
      // lib.optionalAttrs (cfg.maxBackups != null) { GJ_MAX_BACKUPS = toString cfg.maxBackups; }
      // lib.optionalAttrs (cfg.s3.bucket != null) { GJ_S3_BUCKET = cfg.s3.bucket; }
      // lib.optionalAttrs (cfg.s3.endpoint != null) { GJ_S3_ENDPOINT = cfg.s3.endpoint; }
      // lib.optionalAttrs (cfg.s3.region != null) { GJ_S3_REGION = cfg.s3.region; }
      // lib.optionalAttrs (cfg.s3.pathStyle) { GJ_S3_PATH_STYLE = "true"; }
      // lib.optionalAttrs (!cfg.s3.useSSL) { GJ_S3_USE_SSL = "false"; }
      // lib.optionalAttrs (cfg.grpcTLS.caFile != null) { GJ_GRPC_CA = toString cfg.grpcTLS.caFile; }
      // lib.optionalAttrs (cfg.grpcTLS.certFile != null) { GJ_GRPC_CERT = toString cfg.grpcTLS.certFile; }
      // lib.optionalAttrs (cfg.grpcTLS.keyFile != null) { GJ_GRPC_KEY = toString cfg.grpcTLS.keyFile; };
  in {
    assertions = [
      {
        assertion = cfg.role == "worker" -> cfg.controller != null;
        message = "services.gamejanitor.controller must be set when role is 'worker'.";
      }
      {
        assertion = cfg.role == "worker" -> cfg.workerTokenFile != null;
        message = "services.gamejanitor.workerTokenFile must be set when role is 'worker'.";
      }
      {
        assertion = (cfg.role != "standalone") -> cfg.grpcPort != null;
        message = "services.gamejanitor.grpcPort must be set for multi-node roles (controller, worker, controller+worker).";
      }
    ];

    virtualisation.docker.enable = lib.mkIf hasLocalWorker true;

    systemd.services.gamejanitor = {
      description = "Gamejanitor Game Server Manager";
      after = [ "network.target" ] ++ lib.optional hasLocalWorker "docker.service";
      wants = lib.optional hasLocalWorker "docker.service";
      wantedBy = [ "multi-user.target" ];

      environment = settingsEnv // cfg.environment;

      script = let
        args = lib.cli.toGNUCommandLineShell {} ({
          role = cfg.role;
          bind = cfg.bindAddress;
          port = cfg.port;
          data-dir = cfg.dataDir;
        }
        // lib.optionalAttrs (cfg.grpcPort != null) { grpc-port = cfg.grpcPort; }
        // lib.optionalAttrs (cfg.sftpPort != null && hasWebUI) { sftp-port = cfg.sftpPort; }
        // lib.optionalAttrs (cfg.controller != null) { controller = cfg.controller; }
        // lib.optionalAttrs (cfg.workerId != null) { worker-id = cfg.workerId; });
      in ''
        ${lib.optionalString (cfg.workerTokenFile != null) ''
          export GJ_WORKER_TOKEN="$(cat ${lib.escapeShellArg cfg.workerTokenFile})"
        ''}
        ${lib.optionalString (cfg.s3.accessKeyFile != null) ''
          export GJ_S3_ACCESS_KEY="$(cat ${lib.escapeShellArg cfg.s3.accessKeyFile})"
        ''}
        ${lib.optionalString (cfg.s3.secretKeyFile != null) ''
          export GJ_S3_SECRET_KEY="$(cat ${lib.escapeShellArg cfg.s3.secretKeyFile})"
        ''}
        exec ${cfg.package}/bin/gamejanitor serve ${args}
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
        lib.optional hasWebUI cfg.port
        ++ lib.optional (cfg.grpcPort != null) cfg.grpcPort
        ++ lib.optional (cfg.sftpPort != null && hasWebUI) cfg.sftpPort;
      allowedTCPPortRanges = lib.optional hasLocalWorker { from = portRangeStart; to = portRangeEnd; };
      allowedUDPPortRanges = lib.optional hasLocalWorker { from = portRangeStart; to = portRangeEnd; };
    };
  });
}
