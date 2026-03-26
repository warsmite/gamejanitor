package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// --- Cluster config file ---

type clusterEntry struct {
	Address string `yaml:"address"`
	Token   string `yaml:"token,omitempty"`
}

type clustersConfig struct {
	Current  string                  `yaml:"current,omitempty"`
	Clusters map[string]clusterEntry `yaml:"clusters,omitempty"`
}

func clustersConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gamejanitor", "clusters.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gamejanitor", "clusters.yaml")
}

func loadClustersConfig() (*clustersConfig, error) {
	path := clustersConfigPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &clustersConfig{Clusters: map[string]clusterEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg clustersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Clusters == nil {
		cfg.Clusters = map[string]clusterEntry{}
	}
	return &cfg, nil
}

func saveClustersConfig(cfg *clustersConfig) error {
	path := clustersConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// resolveClusterContext resolves the API URL and auth token from cluster config.
// Priority: CLI flags > named cluster (--cluster) > current cluster > defaults.
func resolveClusterContext() (resolvedURL, resolvedToken string) {
	resolvedURL = apiURL
	resolvedToken = authToken

	// If both --api-url and --token were explicitly set, skip cluster resolution
	if rootCmd.PersistentFlags().Changed("api-url") && rootCmd.PersistentFlags().Changed("token") {
		return
	}

	// Determine which cluster to look up
	name := clusterName
	if name == "" {
		cfg, err := loadClustersConfig()
		if err != nil {
			return
		}
		name = cfg.Current
	}

	if name == "" {
		return
	}

	cfg, err := loadClustersConfig()
	if err != nil {
		return
	}

	cluster, ok := cfg.Clusters[name]
	if !ok {
		return
	}

	// Only override values that weren't explicitly set via CLI flags
	if !rootCmd.PersistentFlags().Changed("api-url") {
		resolvedURL = cluster.Address
	}
	if !rootCmd.PersistentFlags().Changed("token") {
		resolvedToken = cluster.Token
	}
	return
}

// --- Cluster commands ---

var clusterCmd = &cobra.Command{
	Use:     "cluster",
	Aliases: []string{"ctx"},
	Short:   "Manage cluster connections",
}

func init() {
	clusterCmd.AddCommand(clusterAddCmd, clusterUseCmd, clusterListCmd, clusterRemoveCmd, clusterCurrentCmd)

	clusterAddCmd.Flags().String("address", "", "Cluster API address (e.g. https://gj.example.com)")
	clusterAddCmd.Flags().String("token", "", "Auth token for this cluster")
	clusterAddCmd.MarkFlagRequired("address")
}

var clusterAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a cluster connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		address, _ := cmd.Flags().GetString("address")
		token, _ := cmd.Flags().GetString("token")

		cfg, err := loadClustersConfig()
		if err != nil {
			return err
		}

		cfg.Clusters[name] = clusterEntry{
			Address: address,
			Token:   token,
		}

		// If this is the first cluster, make it current
		if len(cfg.Clusters) == 1 {
			cfg.Current = name
		}

		if err := saveClustersConfig(cfg); err != nil {
			return err
		}

		fmt.Printf("Cluster %q added.\n", name)
		if cfg.Current == name {
			fmt.Printf("Switched to cluster %q.\n", name)
		}
		return nil
	},
}

var clusterUseCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Switch to a cluster (no argument clears to localhost default)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadClustersConfig()
		if err != nil {
			return err
		}

		// No argument: clear current context
		if len(args) == 0 {
			cfg.Current = ""
			if err := saveClustersConfig(cfg); err != nil {
				return err
			}
			fmt.Println("Cluster context cleared. Using default (http://localhost:8080).")
			return nil
		}

		name := args[0]

		if _, ok := cfg.Clusters[name]; !ok {
			return exitError(fmt.Errorf("cluster %q not found\n  Run 'gamejanitor cluster list' to see available clusters", name))
		}

		cfg.Current = name
		if err := saveClustersConfig(cfg); err != nil {
			return err
		}

		fmt.Printf("Switched to cluster %q.\n", name)
		return nil
	},
}

var clusterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cluster connections",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadClustersConfig()
		if err != nil {
			return err
		}

		if len(cfg.Clusters) == 0 {
			fmt.Println("No clusters configured.")
			fmt.Println("  Add one with: gamejanitor cluster add <name> --address <url> --token <token>")
			fmt.Println("  Or just use 'gamejanitor serve' for local mode (default: http://localhost:8080)")
			return nil
		}

		if jsonOutput {
			printJSON(map[string]any{
				"current":  cfg.Current,
				"clusters": cfg.Clusters,
			})
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "  \tNAME\tADDRESS\tTOKEN")
		for name, c := range cfg.Clusters {
			marker := " "
			if name == cfg.Current {
				marker = "*"
			}
			tokenDisplay := "(none)"
			if c.Token != "" {
				tokenDisplay = c.Token[:8] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", marker, name, c.Address, tokenDisplay)
		}
		w.Flush()
		return nil
	},
}

var clusterRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a cluster connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := loadClustersConfig()
		if err != nil {
			return err
		}

		if _, ok := cfg.Clusters[name]; !ok {
			return exitError(fmt.Errorf("cluster %q not found", name))
		}

		delete(cfg.Clusters, name)
		if cfg.Current == name {
			cfg.Current = ""
		}

		if err := saveClustersConfig(cfg); err != nil {
			return err
		}

		fmt.Printf("Cluster %q removed.\n", name)
		if name == cfg.Current {
			fmt.Println("No current cluster set. Using default (http://localhost:8080).")
		}
		return nil
	},
}

var clusterCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the current cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadClustersConfig()
		if err != nil {
			return err
		}

		if cfg.Current == "" {
			fmt.Println("No cluster selected. Using default (http://localhost:8080).")
			return nil
		}

		c, ok := cfg.Clusters[cfg.Current]
		if !ok {
			fmt.Printf("Current cluster %q no longer exists in config.\n", cfg.Current)
			return nil
		}

		if jsonOutput {
			printJSON(map[string]any{
				"name":    cfg.Current,
				"address": c.Address,
			})
			return nil
		}

		fmt.Printf("%s (%s)\n", cfg.Current, c.Address)
		return nil
	},
}

