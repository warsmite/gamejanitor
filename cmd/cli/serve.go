package cli

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/0xkowalskidev/gamejanitor/internal/config"
	"github.com/0xkowalskidev/gamejanitor/internal/db"
	"github.com/0xkowalskidev/gamejanitor/internal/db/seed"
	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Gamejanitor HTTP server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().IntP("port", "p", 8080, "Port to listen on")
	serveCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory for database and backups")
}

func runServe(cmd *cobra.Command, args []string) error {
	port, _ := cmd.Flags().GetInt("port")
	dataDir, _ := cmd.Flags().GetString("data-dir")

	cfg := config.Config{
		Port:    port,
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "gamejanitor.db"),
	}

	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	logger.Info("opening database", "path", cfg.DBPath)
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	logger.Info("running migrations")
	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	logger.Info("seeding game data")
	if err := seed.SeedGames(database); err != nil {
		return fmt.Errorf("failed to seed games: %w", err)
	}

	dockerClient, err := docker.New(logger)
	if err != nil {
		return fmt.Errorf("failed to connect to docker: %w", err)
	}
	defer dockerClient.Close()
	_ = dockerClient // used in later phases

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	logger.Info("starting gamejanitor", "port", cfg.Port, "data_dir", cfg.DataDir, "db_path", cfg.DBPath)

	return http.ListenAndServe(addr, mux)
}
