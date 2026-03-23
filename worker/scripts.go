package worker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/games"
)

func prepareGameScripts(gameStore *games.GameStore, dataDir, gameID, gameserverID string) (string, string, error) {
	gsDir := filepath.Join(dataDir, "gameservers", gameserverID)
	if err := gameStore.ExtractScripts(gameID, gsDir); err != nil {
		return "", "", fmt.Errorf("extracting scripts for %s: %w", gameserverID, err)
	}

	scriptDir := filepath.Join(gsDir, "scripts")
	defaultsDir := filepath.Join(gsDir, "defaults")

	if _, err := os.Stat(defaultsDir); err != nil {
		defaultsDir = ""
	}

	return scriptDir, defaultsDir, nil
}
