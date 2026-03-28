package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var initConfigCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a starter config file",
	Long:  "Generates a gamejanitor.yaml in the current directory with sensible defaults.",
	RunE:  runInit,
}

func init() {
	initConfigCmd.Flags().String("profile", "newbie", "Config profile: newbie or business")
}

func runInit(cmd *cobra.Command, args []string) error {
	profile, _ := cmd.Flags().GetString("profile")

	var content string
	switch profile {
	case "newbie":
		content = newbieConfig
	case "business":
		content = businessConfig
	default:
		return exitError(fmt.Errorf("unknown profile %q (use newbie or business)", profile))
	}

	outPath := "gamejanitor.yaml"
	if _, err := os.Stat(outPath); err == nil {
		if !confirmAction(fmt.Sprintf("%s already exists. Overwrite?", outPath)) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	fmt.Printf("Config written to %s (profile: %s)\n", outPath, profile)
	fmt.Printf("Start with: gamejanitor serve --config %s\n", outPath)
	return nil
}

const newbieConfig = `# Gamejanitor configuration
# This is optional — gamejanitor works without a config file.
# Uncomment and edit settings as needed.

# Network — where gamejanitor listens
# bind: 127.0.0.1          # listen address (0.0.0.0 for all interfaces)
# port: 8080                # HTTP API and web UI port
# sftp_port: 2222           # SFTP file server port (0 to disable)

# Storage
# data_dir: ~/.local/share/gamejanitor   # database, backups, and config

# Backup storage — default is local ({data_dir}/backups)
# For S3-compatible storage (AWS, MinIO, Backblaze B2, Wasabi):
# backup_store:
#   type: s3
#   endpoint: s3.us-east-1.amazonaws.com
#   bucket: my-backups
#   region: us-east-1
#   # Set access/secret keys via environment variables:
#   #   GJ_BACKUP_STORE_ACCESS_KEY and GJ_BACKUP_STORE_SECRET_KEY

# Third-party API keys
# steam_api_key: ""           # Steam Web API key (for Workshop mod search)
#                              # Get one at: https://steamcommunity.com/dev/apikey
#                              # Or set via: GJ_STEAM_API_KEY env var

# Runtime settings — these are written to the database on startup.
# You can also change them at any time via the web UI or API.
# settings:
#   max_backups: 10           # max backups per gameserver
#   port_mode: auto           # auto or manual port allocation
#   port_range_start: 27000   # port range for auto-allocation
#   port_range_end: 28999
`

const businessConfig = `# Gamejanitor configuration — business profile
# Deploy this via Ansible/Terraform. Settings block is source of truth:
# values are written to DB on every startup, overwriting runtime changes.

bind: 0.0.0.0
port: 8080
grpc_port: 9090
sftp_port: 2222
data_dir: /var/lib/gamejanitor

# Components — set one to false for multi-node deployments
controller: true
worker: true

# Multi-node worker settings (uncomment for worker-only nodes)
# controller_address: 10.0.0.1:9090
# worker_id: worker-1
# worker_token: gj_worker_...    # or set GJ_WORKER_TOKEN env var

# Worker capacity (required for multi-node workers)
# worker_limits:
#   max_memory_mb: 32768
#   max_cpu: 16
#   max_storage_mb: 500000
#   port_range_start: 27000
#   port_range_end: 27999

# TLS for gRPC (controller <-> worker)
# Generate certs with: gamejanitor gen-worker-cert <worker-id>
# tls:
#   ca: /etc/gamejanitor/certs/ca.pem
#   cert: /etc/gamejanitor/certs/cert.pem
#   key: /etc/gamejanitor/certs/key.pem

# Backup storage
backup_store:
  type: s3
  endpoint: s3.us-east-1.amazonaws.com
  bucket: company-gamejanitor-backups
  region: us-east-1
  # Set credentials via environment:
  #   GJ_BACKUP_STORE_ACCESS_KEY and GJ_BACKUP_STORE_SECRET_KEY

# Runtime settings — written to DB on every startup
settings:
  auth_enabled: true
  localhost_bypass: false
  max_backups: 5
  require_memory_limit: true
  require_cpu_limit: true
  require_storage_limit: true
  rate_limit_enabled: true
  rate_limit_per_ip: 20
  rate_limit_per_token: 10
  rate_limit_login: 10
  event_retention_days: 30
  port_range_start: 27000
  port_range_end: 28999
  port_mode: auto
`
