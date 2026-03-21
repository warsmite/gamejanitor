package worker

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

// PlacementCandidate is a worker ranked for gameserver placement.
type PlacementCandidate struct {
	Worker Worker
	NodeID string
	Score  float64
}

// Dispatcher routes operations to the correct Worker for a given gameserver.
// In standalone mode, all operations go to a single LocalWorker.
// In multi-node mode, routes based on gameserver-to-node assignment.
type Dispatcher struct {
	local    Worker    // nil if controller-only (no local Docker)
	registry *Registry // nil in standalone mode
	db       *sql.DB   // nil in standalone mode (no node lookups needed)
	log      *slog.Logger
}

// NewLocalDispatcher creates a standalone dispatcher that routes everything to a local worker.
func NewLocalDispatcher(w Worker) *Dispatcher {
	return &Dispatcher{local: w}
}

// NewMultiNodeDispatcher creates a dispatcher that routes to local or remote workers
// based on the gameserver's node_id in the database.
func NewMultiNodeDispatcher(local Worker, registry *Registry, db *sql.DB, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		local:    local,
		registry: registry,
		db:       db,
		log:      log,
	}
}

// WorkerFor returns the Worker responsible for an existing gameserver.
// In standalone mode, always returns the local worker.
// In multi-node mode, looks up the gameserver's node_id and routes accordingly.
func (d *Dispatcher) WorkerFor(gameserverID string) Worker {
	if d.registry == nil {
		return d.local
	}

	nodeID, err := d.lookupNodeID(gameserverID)
	if err != nil {
		d.log.Error("looking up node for gameserver, falling back to local", "gameserver_id", gameserverID, "error", err)
		return d.local
	}

	// Empty node_id means local
	if nodeID == "" {
		if d.local != nil {
			return d.local
		}
		d.log.Error("gameserver assigned to local node but no local worker available", "gameserver_id", gameserverID)
		return nil
	}

	w, ok := d.registry.Get(nodeID)
	if !ok {
		d.log.Error("worker not found in registry", "node_id", nodeID, "gameserver_id", gameserverID)
		return nil
	}
	return w
}

// RankWorkersForPlacement returns all connected workers ranked by placement score (best first).
// Scores by minimum headroom percentage across memory and CPU limits.
// Uses allocated (sum of limits for assigned gameservers), not live usage,
// to avoid overcommit when stopped servers are started.
func (d *Dispatcher) RankWorkersForPlacement() []PlacementCandidate {
	if d.registry == nil {
		if d.local != nil {
			return []PlacementCandidate{{Worker: d.local, NodeID: "", Score: 0}}
		}
		return nil
	}

	workers := d.registry.ListWorkers()
	if len(workers) == 0 {
		if d.local != nil {
			return []PlacementCandidate{{Worker: d.local, NodeID: "", Score: 0}}
		}
		d.log.Error("no workers available for gameserver placement")
		return nil
	}

	var candidates []PlacementCandidate

	for _, info := range workers {
		allocMem, err := models.AllocatedMemoryByNode(d.db, info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated memory for worker", "worker_id", info.ID, "error", err)
			continue
		}
		allocCPU, err := models.AllocatedCPUByNode(d.db, info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated CPU for worker", "worker_id", info.ID, "error", err)
			continue
		}
		allocStorage, err := models.AllocatedStorageByNode(d.db, info.ID)
		if err != nil {
			d.log.Warn("failed to query allocated storage for worker", "worker_id", info.ID, "error", err)
			continue
		}

		node, _ := models.GetWorkerNode(d.db, info.ID)

		if node != nil && node.Cordoned {
			d.log.Debug("skipping cordoned worker for placement", "worker_id", info.ID)
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
			// No limits set — rank by least allocated memory (negative so less = better)
			score = -float64(allocMem)
		}

		w, ok := d.registry.Get(info.ID)
		if !ok {
			continue
		}
		candidates = append(candidates, PlacementCandidate{Worker: w, NodeID: info.ID, Score: score})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) == 0 {
		d.log.Warn("no suitable worker found, falling back to local")
		if d.local != nil {
			return []PlacementCandidate{{Worker: d.local, NodeID: "", Score: 0}}
		}
	}

	return candidates
}

// SelectWorkerByNodeID returns the Worker for a specific node ID.
// Used when the user explicitly chooses a node for placement.
func (d *Dispatcher) SelectWorkerByNodeID(nodeID string) (Worker, error) {
	if nodeID == "" {
		if d.local != nil {
			return d.local, nil
		}
		return nil, fmt.Errorf("no local worker available")
	}

	if d.registry == nil {
		return nil, fmt.Errorf("multi-node not enabled")
	}

	w, ok := d.registry.Get(nodeID)
	if !ok {
		return nil, fmt.Errorf("worker %s is not connected", nodeID)
	}
	return w, nil
}

// ListWorkers returns info for all registered workers. Returns nil in standalone mode.
func (d *Dispatcher) ListWorkers() []WorkerInfo {
	if d.registry == nil {
		return nil
	}
	return d.registry.ListWorkers()
}

func (d *Dispatcher) lookupNodeID(gameserverID string) (string, error) {
	if d.db == nil {
		return "", nil
	}
	gs, err := models.GetGameserver(d.db, gameserverID)
	if err != nil {
		return "", fmt.Errorf("looking up node_id for gameserver %s: %w", gameserverID, err)
	}
	if gs == nil || gs.NodeID == nil {
		return "", nil
	}
	return *gs.NodeID, nil
}
