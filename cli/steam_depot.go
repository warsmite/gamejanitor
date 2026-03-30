package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/warsmite/gamejanitor/steam"
)

func init() {
	depotDownloadCmd.Flags().Uint32("app", 0, "Steam app ID to download (required)")
	depotDownloadCmd.Flags().String("branch", "public", "Branch to download")
	depotDownloadCmd.Flags().StringP("data-dir", "d", "", "Data directory (default: ~/.local/share/gamejanitor)")
	depotDownloadCmd.MarkFlagRequired("app")

	depotListCmd.Flags().StringP("data-dir", "d", "", "Data directory")
	depotClearCmd.Flags().Uint32("app", 0, "App ID to clear (clears all if not specified)")
	depotClearCmd.Flags().StringP("data-dir", "d", "", "Data directory")

	steamCmd.AddCommand(depotCmd)
	depotCmd.AddCommand(depotDownloadCmd)
	depotCmd.AddCommand(depotListCmd)
	depotCmd.AddCommand(depotClearCmd)
}

var depotCmd = &cobra.Command{
	Use:   "depot",
	Short: "Manage Steam depot cache (dev/debug)",
}

var depotDownloadCmd = &cobra.Command{
	Use:   "download",
	Short: "Download a Steam depot to the local cache",
	Example: `  gamejanitor steam depot download --app 258550          # Rust dedicated server
  gamejanitor steam depot download --app 380870          # Project Zomboid
  gamejanitor steam depot download --app 896660          # Valheim`,
	RunE: func(cmd *cobra.Command, args []string) error {
		appID, _ := cmd.Flags().GetUint32("app")
		branch, _ := cmd.Flags().GetString("branch")
		dataDir := resolveDataDir(cmd)

		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

		// Try to read credentials from the running server's settings DB
		accountName, refreshToken := loadSteamCredentials(dataDir)

		creds := &cliCredentials{account: accountName, token: refreshToken}
		svc := steam.NewService(log, dataDir, creds)
		defer svc.Close()

		dlCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		fmt.Fprintf(os.Stderr, "Downloading app %d (branch: %s) to %s\n", appID, branch, filepath.Join(dataDir, "cache", "depots"))

		start := time.Now()
		result, err := svc.EnsureDepot(dlCtx, appID, branch, func(completedBytes, totalBytes uint64, completedChunks, totalChunks int) {
			pct := float64(completedBytes) / float64(totalBytes) * 100
			fmt.Fprintf(os.Stderr, "\r  %5.1f%%  %d/%d chunks  %d MB / %d MB",
				pct, completedChunks, totalChunks,
				completedBytes/1024/1024, totalBytes/1024/1024)
		})
		if err != nil {
			return exitError(err)
		}
		fmt.Fprintln(os.Stderr)

		elapsed := time.Since(start)
		if result.Cached {
			fmt.Printf("Already up to date (cached)\n")
			fmt.Printf("Path: %s\n", result.DepotDir)
		} else {
			fmt.Printf("Downloaded %d MB in %s\n", result.BytesDownloaded/1024/1024, elapsed.Truncate(time.Second))
			fmt.Printf("Path: %s\n", result.DepotDir)
		}
		return nil
	},
}

var depotListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cached depots",
	RunE: func(cmd *cobra.Command, args []string) error {
		dataDir := resolveDataDir(cmd)
		cacheDir := filepath.Join(dataDir, "cache", "depots")

		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No cached depots.")
				return nil
			}
			return exitError(err)
		}

		if len(entries) == 0 {
			fmt.Println("No cached depots.")
			return nil
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			appDir := filepath.Join(cacheDir, e.Name())

			// Read metadata from any depot subdir
			metaFiles, _ := filepath.Glob(filepath.Join(appDir, "*/manifest_meta.json"))
			mergedDir := filepath.Join(appDir, "merged")
			var size int64
			filepath.Walk(mergedDir, func(_ string, info os.FileInfo, _ error) error {
				if info != nil && !info.IsDir() {
					size += info.Size()
				}
				return nil
			})

			status := "incomplete"
			if len(metaFiles) > 0 {
				status = "cached"
			}

			fmt.Printf("App %-10s  %s  %d MB\n", e.Name(), status, size/1024/1024)
		}
		return nil
	},
}

var depotClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear cached depot files",
	RunE: func(cmd *cobra.Command, args []string) error {
		dataDir := resolveDataDir(cmd)
		appID, _ := cmd.Flags().GetUint32("app")
		cacheDir := filepath.Join(dataDir, "cache", "depots")

		if appID != 0 {
			target := filepath.Join(cacheDir, fmt.Sprintf("%d", appID))
			if err := os.RemoveAll(target); err != nil {
				return exitError(fmt.Errorf("failed to clear app %d: %w", appID, err))
			}
			fmt.Printf("Cleared cache for app %d\n", appID)
		} else {
			if !confirmAction("Clear ALL cached depots?") {
				return nil
			}
			if err := os.RemoveAll(cacheDir); err != nil {
				return exitError(fmt.Errorf("failed to clear cache: %w", err))
			}
			fmt.Println("All depot caches cleared.")
		}
		return nil
	},
}

func resolveDataDir(cmd *cobra.Command) string {
	d, _ := cmd.Flags().GetString("data-dir")
	if d != "" {
		return d
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".local", "share", "gamejanitor")
	}
	return "/var/lib/gamejanitor"
}

// loadSteamCredentials reads Steam credentials from the settings DB.
func loadSteamCredentials(dataDir string) (accountName, refreshToken string) {
	dbPath := filepath.Join(dataDir, "gamejanitor.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "", ""
	}

	// Quick read from SQLite — avoid importing the full store/settings stack
	// by using the SDK to call the running server's API instead.
	// If no server is running, we fall back to anonymous.
	sdkClient := getClient()
	settings, err := sdkClient.Settings.Get(context.Background())
	if err != nil {
		return "", ""
	}

	var account, token string
	if raw, ok := settings["steam_account_name"]; ok {
		json_Unmarshal(raw, &account)
	}
	if raw, ok := settings["steam_refresh_token"]; ok {
		json_Unmarshal(raw, &token)
	}
	return account, token
}

// json_Unmarshal is a thin wrapper to avoid importing encoding/json just for this.
func json_Unmarshal(data []byte, v *string) {
	// RawMessage is already a JSON string like `"value"`, strip quotes
	s := string(data)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		*v = s[1 : len(s)-1]
	}
}

type cliCredentials struct {
	account string
	token   string
}

func (c *cliCredentials) SteamAccountName() string  { return c.account }
func (c *cliCredentials) SteamRefreshToken() string { return c.token }
