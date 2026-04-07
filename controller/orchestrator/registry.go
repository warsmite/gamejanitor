package orchestrator

import (
	"github.com/warsmite/gamejanitor/worker"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

// RegistryStore defines the persistence methods the registry needs for
// worker node lifecycle tracking.
type RegistryStore interface {
	ResetAllWorkerStatus(status string) error
	ListWorkerNodes() ([]model.WorkerNode, error)
	SetWorkerNodeStatus(id string, status string) error
}

// WorkerInfo tracks a connected worker's metadata and status.
type WorkerInfo struct {
	ID                string
	LanIP             string
	ExternalIP        string
	CPUCores          int64
	MemoryTotalMB     int64
	MemoryAvailableMB int64
	DiskTotalMB       int64
	DiskAvailableMB   int64
	Status            string
	LastSeen          time.Time
	TokenID           string
}

// Registry tracks all workers (local and remote).
// Online workers have an active gRPC connection. Offline workers are tracked
// in the database but have no connection — they appear in listings with their
// persisted metadata so operators can see all known workers.
type Registry struct {
	workers map[string]*registeredWorker
	mu      sync.RWMutex
	store   RegistryStore
	log     *slog.Logger

	// Callbacks fired on state transitions
	onOnline  func(nodeID string, w worker.Worker)
	onOffline func(nodeID string)
}

type registeredWorker struct {
	worker worker.Worker // nil for offline workers
	info   WorkerInfo
}

func NewRegistry(store RegistryStore, log *slog.Logger) *Registry {
	return &Registry{
		workers: make(map[string]*registeredWorker),
		store:   store,
		log:     log,
	}
}

// LoadFromDB populates the registry with all known workers from the database,
// setting them all to offline. Called on controller startup so the controller
// immediately knows about all workers before they reconnect.
func (r *Registry) LoadFromDB() error {
	if r.store == nil {
		return nil
	}

	// Reset all workers to offline — they must heartbeat to prove they're alive
	if err := r.store.ResetAllWorkerStatus(model.WorkerStatusOffline); err != nil {
		return fmt.Errorf("resetting worker status on startup: %w", err)
	}

	nodes, err := r.store.ListWorkerNodes()
	if err != nil {
		return fmt.Errorf("loading workers from database: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, node := range nodes {
		r.workers[node.ID] = &registeredWorker{
			worker: nil,
			info: WorkerInfo{
				ID:         node.ID,
				LanIP:      node.LanIP,
				ExternalIP: node.ExternalIP,
				Status:     model.WorkerStatusOffline,
			},
		}
	}

	if len(nodes) > 0 {
		r.log.Info("loaded workers from database", "count", len(nodes), "status", "offline")
	}
	return nil
}

// SetCallbacks sets the state transition callbacks.
// Must be called before any workers register.
func (r *Registry) SetCallbacks(onOnline func(string, worker.Worker), onOffline func(string)) {
	r.onOnline = onOnline
	r.onOffline = onOffline
}

// Register adds a worker with an active connection and marks it online.
func (r *Registry) Register(nodeID string, w worker.Worker, info WorkerInfo) {
	r.mu.Lock()

	if old, ok := r.workers[nodeID]; ok && old.worker != nil {
		if closer, ok := old.worker.(interface{ Close() error }); ok {
			closer.Close()
		}
	}

	info.LastSeen = time.Now()
	info.Status = model.WorkerStatusOnline
	r.workers[nodeID] = &registeredWorker{worker: w, info: info}
	r.log.Info("worker online", "worker", nodeID, "lan_ip", info.LanIP)
	r.mu.Unlock()

	// Persist status to DB
	if r.store != nil {
		if err := r.store.SetWorkerNodeStatus(nodeID, model.WorkerStatusOnline); err != nil {
			r.log.Warn("failed to persist worker online status", "worker", nodeID, "error", err)
		}
	}

	if r.onOnline != nil {
		r.onOnline(nodeID, w)
	}
}

// ClearWorker removes the worker's connection from the registry without changing
// its status or triggering the offline callback. Used when the event stream breaks
// — the worker is still running and will re-register on the next heartbeat.
// The nil connection causes UpdateHeartbeat to fail, forcing the heartbeat handler
// to fall through to the dial-back path.
func (r *Registry) ClearWorker(nodeID string) {
	r.mu.Lock()
	rw, ok := r.workers[nodeID]
	if ok {
		if rw.worker != nil {
			if closer, ok := rw.worker.(interface{ Close() error }); ok {
				closer.Close()
			}
		}
		rw.worker = nil
	}
	r.mu.Unlock()
	r.log.Info("worker connection cleared for re-registration", "worker", nodeID)
}

// SetOffline transitions a worker to offline state, closing its connection
// but keeping it in the registry with persisted metadata.
func (r *Registry) SetOffline(nodeID string) {
	r.mu.Lock()

	rw, ok := r.workers[nodeID]
	if !ok {
		r.mu.Unlock()
		return
	}

	if rw.worker != nil {
		if closer, ok := rw.worker.(interface{ Close() error }); ok {
			closer.Close()
		}
	}
	rw.worker = nil
	rw.info.Status = model.WorkerStatusOffline
	r.log.Info("worker offline", "worker", nodeID)
	r.mu.Unlock()

	// Persist status to DB
	if r.store != nil {
		if err := r.store.SetWorkerNodeStatus(nodeID, model.WorkerStatusOffline); err != nil {
			r.log.Warn("failed to persist worker offline status", "worker", nodeID, "error", err)
		}
	}

	if r.onOffline != nil {
		r.onOffline(nodeID)
	}
}

// Unregister removes a worker entirely from the registry.
// Used for permanent removal (e.g. admin action), not for heartbeat timeouts.
func (r *Registry) Unregister(nodeID string) {
	r.mu.Lock()

	rw, ok := r.workers[nodeID]
	if !ok {
		r.mu.Unlock()
		return
	}

	if rw.worker != nil {
		if closer, ok := rw.worker.(interface{ Close() error }); ok {
			closer.Close()
		}
	}
	delete(r.workers, nodeID)
	r.log.Info("worker unregistered", "worker", nodeID)
	r.mu.Unlock()

	if r.onOffline != nil {
		r.onOffline(nodeID)
	}
}

// Get returns the active worker.Worker connection for a node.
// Returns nil, false if the worker is offline or unknown.
func (r *Registry) Get(nodeID string) (worker.Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rw, ok := r.workers[nodeID]
	if !ok || rw.worker == nil {
		return nil, false
	}
	return rw.worker, true
}

// GetInfo returns metadata for a worker regardless of online/offline state.
func (r *Registry) GetInfo(nodeID string) (WorkerInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rw, ok := r.workers[nodeID]
	if !ok {
		return WorkerInfo{}, false
	}
	return rw.info, true
}

// UpdateHeartbeat updates a connected worker's metadata.
func (r *Registry) UpdateHeartbeat(nodeID string, info WorkerInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rw, ok := r.workers[nodeID]
	if !ok || rw.worker == nil {
		return fmt.Errorf("worker %s not online", nodeID)
	}
	info.LastSeen = time.Now()
	info.Status = model.WorkerStatusOnline
	info.TokenID = rw.info.TokenID // preserve token from registration
	rw.info = info
	return nil
}

// ListWorkers returns info for all workers (online and offline).
func (r *Registry) ListWorkers() []WorkerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]WorkerInfo, 0, len(r.workers))
	for _, rw := range r.workers {
		infos = append(infos, rw.info)
	}
	return infos
}

// ListOnlineWorkers returns info for only connected workers.
func (r *Registry) ListOnlineWorkers() []WorkerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var infos []WorkerInfo
	for _, rw := range r.workers {
		if rw.worker != nil {
			infos = append(infos, rw.info)
		}
	}
	return infos
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workers)
}

// StartReaper starts a goroutine that transitions stale workers to offline.
func (r *Registry) StartReaper(ctx context.Context, log *slog.Logger) {
	const heartbeatTimeout = 30 * time.Second

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.reapStale(heartbeatTimeout, log)
			}
		}
	}()
}

func (r *Registry) reapStale(timeout time.Duration, log *slog.Logger) {
	r.mu.RLock()
	var stale []string
	for id, rw := range r.workers {
		// Only reap online workers — offline workers are already offline
		if rw.worker != nil && time.Since(rw.info.LastSeen) > timeout {
			stale = append(stale, id)
		}
	}
	r.mu.RUnlock()

	for _, id := range stale {
		log.Warn("worker heartbeat timeout, setting offline", "worker", id)
		r.SetOffline(id)
	}
}
