package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Mode selects a default settings profile: "" (default/newbie) or "business".
	Mode string `yaml:"mode"`

	// Network
	Bind     string `yaml:"bind"`
	Port     int    `yaml:"port"`
	GRPCPort       int `yaml:"grpc_port"`
	WorkerGRPCPort int `yaml:"worker_grpc_port"`
	SFTPPort       int `yaml:"sftp_port"`
	WebUI    bool   `yaml:"web_ui"`

	// Components
	Controller bool `yaml:"controller"`
	Worker     bool `yaml:"worker"`

	// Storage
	DataDir string `yaml:"data_dir"`

	// Multi-node (worker mode)
	ControllerAddress string `yaml:"controller_address"`
	WorkerID          string `yaml:"worker_id"`
	WorkerToken       string `yaml:"worker_token"`

	// Worker capacity
	WorkerLimits *WorkerLimitsConfig `yaml:"worker_limits"`

	// TLS for gRPC
	TLS *TLSConfig `yaml:"tls"`

	// Container runtime
	ContainerRuntime string `yaml:"container_runtime"` // "docker", "podman", or "auto" (default)
	ContainerSocket  string `yaml:"container_socket"`  // explicit socket path; auto-detected if empty

	// Backup storage
	BackupStore *BackupStoreConfig `yaml:"backup_store"`

	// Runtime settings (written to DB on every startup)
	Settings map[string]any `yaml:"settings"`

	// Derived (not from YAML)
	DBPath string `yaml:"-"`
}

type WorkerLimitsConfig struct {
	MaxMemoryMB  int     `yaml:"max_memory_mb"`
	MaxCPU       float64 `yaml:"max_cpu"`
	MaxStorageMB int     `yaml:"max_storage_mb"`
}

type TLSConfig struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type BackupStoreConfig struct {
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	PathStyle bool   `yaml:"path_style"`
	UseSSL    bool   `yaml:"use_ssl"`
}

func DefaultConfig() Config {
	return Config{
		Bind:       "127.0.0.1",
		Port:       8080,
		GRPCPort:       9090,
		WorkerGRPCPort: 9091,
		SFTPPort:       2222,
		WebUI:      true,
		Controller: true,
		Worker:     true,
		DataDir:    "/var/lib/gamejanitor",
	}
}

// Load reads a YAML config file and merges it over defaults.
// Returns the default config if path is empty.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		cfg.DBPath = cfg.DataDir + "/gamejanitor.db"
		cfg.applyEnvSecrets()
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config file: %w", err)
	}

	cfg.DBPath = cfg.DataDir + "/gamejanitor.db"
	cfg.applyEnvSecrets()
	return cfg, nil
}

// Discover tries default config file locations and returns the path if found.
// Returns empty string if no config file exists (which is fine for newbies).
func Discover() string {
	candidates := []string{
		"./gamejanitor.yaml",
		"./gamejanitor.yml",
		"/etc/gamejanitor/config.yaml",
		"/etc/gamejanitor/config.yml",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// applyEnvSecrets overwrites secret fields from env vars if set.
// Only secrets get env var support — everything else is config file only.
func (c *Config) applyEnvSecrets() {
	if v := os.Getenv("GJ_WORKER_TOKEN"); v != "" {
		c.WorkerToken = v
	}

	if c.BackupStore == nil {
		c.BackupStore = &BackupStoreConfig{Type: "local", UseSSL: true}
	}
	if v := os.Getenv("GJ_BACKUP_STORE_ACCESS_KEY"); v != "" {
		c.BackupStore.AccessKey = v
	}
	if v := os.Getenv("GJ_BACKUP_STORE_SECRET_KEY"); v != "" {
		c.BackupStore.SecretKey = v
	}
}

// HasController returns true if this node runs the controller role.
func (c *Config) HasController() bool {
	return c.Controller
}

// HasWorker returns true if this node runs the worker role.
func (c *Config) HasWorker() bool {
	return c.Worker
}

// WorkerOnly returns true if this is a worker-only node (no controller, no DB).
func (c *Config) WorkerOnly() bool {
	return c.Worker && !c.Controller
}

// ResolveContainerSocket returns the container runtime socket path.
// Auto-detects Docker or Podman sockets if not explicitly configured.
func (c *Config) ResolveContainerSocket() string {
	if c.ContainerSocket != "" {
		return c.ContainerSocket
	}

	switch c.ContainerRuntime {
	case "process":
		return ""
	case "docker":
		return "/var/run/docker.sock"
	case "podman":
		return detectPodmanSocket()
	default:
		// Auto-detect: Podman first (rootless), then Docker
		if path := detectPodmanSocket(); path != "" {
			return path
		}
		if _, err := os.Stat("/var/run/docker.sock"); err == nil {
			return "/var/run/docker.sock"
		}
		return "" // fall back to DOCKER_HOST env var
	}
}

func detectPodmanSocket() string {
	if _, err := os.Stat("/run/podman/podman.sock"); err == nil {
		return "/run/podman/podman.sock"
	}
	rootless := fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid())
	if _, err := os.Stat(rootless); err == nil {
		return rootless
	}
	return ""
}
