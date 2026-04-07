package cluster

import (
	"github.com/warsmite/gamejanitor/worker"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/warsmite/gamejanitor/model"
)

// DispatcherStore defines the persistence methods the dispatcher needs for
// gameserver placement and node-to-worker routing.
type DispatcherStore interface {
	GetGameserver(id string) (*model.Gameserver, error)
	GetWorkerNode(id string) (*model.WorkerNode, error)
	AllocatedMemoryByNode(nodeID string) (int, error)
	AllocatedCPUByNode(nodeID string) (float64, error)
	AllocatedStorageByNode(nodeID string) (int, error)
	GameserverCountByNode(nodeID string) (int, error)
}

// PlacementCandidate is a worker ranked for gameserver 
type PlacementCandidate struct {
	Worker          worker.Worker
	NodeID          string
	Score           float64
	GameserverCount int // tiebreaker: fewer gameservers = preferred
}

// Dispatcher routes operations to the correct worker.Worker for a given gameserver.
// All workers (local and remote) are accessed through the registry.
type Dispatcher struct {
	registry *Registry
	store    DispatcherStore
	log      *slog.Logger
}

func NewDispatcher(registry *Registry, store DispatcherStore, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		registry: registry,
		store:    store,
		log:      log,
	}
}

// WorkerFor returns the worker.Worker responsible for an existing gameserver.
// Looks up the gameserver's node_id and routes to the corresponding worker.
func (d *Dispatcher) WorkerFor(gameserverID string) worker.Worker {
	nodeID, err := d.lookupNodeID(gameserverID)
	if err != nil {
		d.log.Error("looking up node for gameserver", "gameserver", gameserverID, "error", err)
		return nil
	}

	if nodeID == "" {
		d.log.Error("gameserver has no node_id", "gameserver", gameserverID)
		return nil
	}

	w, ok := d.registry.Get(nodeID)
	if !ok {
		d.log.Error("worker not found in registry", "node_id", nodeID, "gameserver", gameserverID)
		return nil
	}
	return w
}

// RankWorkersForPlacement returns all connected workers ranked by placement score (best first).
// Scores by minimum headroom percentage across memory and CPU limits.
// Uses allocated (sum of limits for assigned gameservers), not live usage,
// to avoid overcommit when stopped servers are started.
func (d *Dispatcher) RankWorkersForPlacement(requiredLabels model.Labels) []PlacementCandidate {
	workers := d.registry.ListOnlineWorkers()
	if len(workers) == 0 {
		d.log.Error("no workers available for gameserver placement")
		return nil
	}

	var candidates []PlacementCandidate

	for _, info := range workers {
		// Label filtering: skip workers that don't have all required labels
		if !requiredLabels.IsEmpty() && d.store != nil {
			node, err := d.store.GetWorkerNode(info.ID)
			if err != nil || node == nil {
				continue
			}
			if !node.Tags.HasAll(requiredLabels) {
				d.log.Debug("worker skipped: missing required labels", "worker", info.ID, "required", requiredLabels, "has", node.Tags)
				continue
			}
		}
		allocMem, err := d.store.AllocatedMemoryByNode(info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated memory for worker", "worker", info.ID, "error", err)
			continue
		}
		allocCPU, err := d.store.AllocatedCPUByNode(info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated CPU for worker", "worker", info.ID, "error", err)
			continue
		}
		allocStorage, err := d.store.AllocatedStorageByNode(info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated storage for worker", "worker", info.ID, "error", err)
			continue
		}

		node, _ := d.store.GetWorkerNode(info.ID)

		if node != nil && node.Cordoned {
			d.log.Debug("skipping cordoned worker for placement", "worker", info.ID)
			continue
		}

		hasLimits := false
		score := math.MaxFloat64

		if node != nil && node.MaxMemoryMB != nil && *node.MaxMemoryMB > 0 {
			hasLimits = true
			memPct := float64(*node.MaxMemoryMB-allocMem) / float64(*node.MaxMemoryMB)
			if memPct < score {
				score = memPct
			}
		}
		if node != nil && node.MaxCPU != nil && *node.MaxCPU > 0 {
			hasLimits = true
			cpuPct := (*node.MaxCPU - allocCPU) / *node.MaxCPU
			if cpuPct < score {
				score = cpuPct
			}
		}
		if node != nil && node.MaxStorageMB != nil && *node.MaxStorageMB > 0 {
			hasLimits = true
			storagePct := float64(*node.MaxStorageMB-allocStorage) / float64(*node.MaxStorageMB)
			if storagePct < score {
				score = storagePct
			}
		}

		if !hasLimits {
			score = -float64(allocMem)
		}

		gsCount, err := d.store.GameserverCountByNode(info.ID)
		if err != nil {
			d.log.Warn("failed to query gameserver count for worker", "worker", info.ID, "error", err)
		}

		w, ok := d.registry.Get(info.ID)
		if !ok {
			continue
		}
		candidates = append(candidates, PlacementCandidate{Worker: w, NodeID: info.ID, Score: score, GameserverCount: gsCount})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		// Tiebreaker: spread across nodes — fewer gameservers wins
		return candidates[i].GameserverCount < candidates[j].GameserverCount
	})

	return candidates
}

// SelectWorkerByNodeID returns the worker.Worker for a specific node ID.
// Used when the user explicitly chooses a node for 
func (d *Dispatcher) SelectWorkerByNodeID(nodeID string) (worker.Worker, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}

	w, ok := d.registry.Get(nodeID)
	if !ok {
		return nil, fmt.Errorf("worker %s is not connected", nodeID)
	}
	return w, nil
}

// ListWorkers returns info for all registered workers.
func (d *Dispatcher) ListWorkers() []WorkerInfo {
	return d.registry.ListWorkers()
}

func (d *Dispatcher) lookupNodeID(gameserverID string) (string, error) {
	if d.store == nil {
		return "", nil
	}
	gs, err := d.store.GetGameserver(gameserverID)
	if err != nil {
		return "", fmt.Errorf("looking up node_id for gameserver %s: %w", gameserverID, err)
	}
	if gs == nil || gs.NodeID == nil {
		return "", nil
	}
	return *gs.NodeID, nil
}
