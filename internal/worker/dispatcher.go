package worker

import (
	"database/sql"
	"fmt"
	"log/slog"
)

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

// DefaultWorker returns the Worker for new gameservers.
// In standalone mode, returns the local worker.
// In multi-node mode, picks the worker with the most available resources.
// Returns nil if no workers are available (controller-only with no remote workers).
func (d *Dispatcher) DefaultWorker() Worker {
	if d.registry == nil {
		return d.local
	}

	// If we have remote workers, pick the best one
	if d.registry.Count() > 0 {
		w, _, err := d.registry.BestWorker()
		if err == nil {
			return w
		}
		d.log.Warn("failed to pick best worker, falling back to local", "error", err)
	}

	if d.local == nil {
		d.log.Error("no workers available for gameserver placement")
		return nil
	}

	return d.local
}

// DefaultWorkerNodeID returns the node_id for new gameserver placement.
// Returns "" for local worker.
func (d *Dispatcher) DefaultWorkerNodeID() string {
	if d.registry == nil {
		return ""
	}

	if d.registry.Count() > 0 {
		_, nodeID, err := d.registry.BestWorker()
		if err == nil {
			return nodeID
		}
	}

	return ""
}

func (d *Dispatcher) lookupNodeID(gameserverID string) (string, error) {
	if d.db == nil {
		return "", nil
	}
	var nodeID sql.NullString
	err := d.db.QueryRow("SELECT node_id FROM gameservers WHERE id = ?", gameserverID).Scan(&nodeID)
	if err != nil {
		return "", fmt.Errorf("querying node_id for gameserver %s: %w", gameserverID, err)
	}
	return nodeID.String, nil
}
