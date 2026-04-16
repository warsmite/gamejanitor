package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// LocalWorker implements Worker using crun (OCI runtime) with pasta for
// network namespace connectivity. No external daemon required.
type LocalWorker struct {
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string
	resolve VolumeResolver
	rt      *runtime.Runtime

	mu        sync.Mutex
	instances map[string]*managedInstance

	pullMu sync.Mutex // serializes image pulls to prevent index corruption

	tracker *InstanceTracker
}

type managedInstance struct {
	id        string
	name      string
	image     string
	startedAt time.Time
	exitCode  atomic.Int32
	exited    atomic.Bool
	logWriter *rotatingWriter
	done      chan struct{}
	handle    *runtime.ContainerHandle
}

// instanceState is persisted alongside the manifest so running instances
// can be re-adopted after a gamejanitor restart.
type instanceState struct {
	StartedAt time.Time `json:"started_at"`
}

func saveInstanceState(dir string, state instanceState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling instance state: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), data, 0644)
}

func loadInstanceState(dir string) (*instanceState, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state instanceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing instance state: %w", err)
	}
	return &state, nil
}

// instanceManifest is persisted to disk so StartInstance can reconstruct the config.
type instanceManifest struct {
	Name          string               `json:"name"`
	Image         string               `json:"image"`
	Env           []string             `json:"env"`
	Ports         []worker.PortBinding `json:"ports"`
	VolumeName    string               `json:"volume_name"`
	MemoryLimitMB int                  `json:"memory_limit_mb"`
	CPULimit      float64              `json:"cpu_limit"`
	Binds         []string             `json:"binds"`
	Entrypoint    []string             `json:"entrypoint,omitempty"`
}

func New(gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	cleanupOverlayMounts(dataDir, log)

	rt, err := runtime.New(dataDir, log)
	if err != nil {
		log.Error("crun runtime initialization failed", "error", err)
	}

	w := &LocalWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		rt:        rt,
		instances: make(map[string]*managedInstance),
	}
	w.tracker = NewInstanceTracker(log)
	w.resolve = w.volumeResolver()
	w.recoverInstances()

	log.Info("runtime ready",
		"crun", rt != nil,
		"root", os.Getuid() == 0)
	return w
}

func (w *LocalWorker) volumeResolver() VolumeResolver {
	return func(ctx context.Context, volumeName string) (string, error) {
		path := filepath.Join(w.dataDir, "volumes", volumeName)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("volume %s not found: %w", volumeName, err)
		}
		return path, nil
	}
}

func (w *LocalWorker) imagesDir() string  { return filepath.Join(w.dataDir, "images") }
func (w *LocalWorker) instanceDir(id string) string {
	return filepath.Join(w.dataDir, "instances", id)
}
