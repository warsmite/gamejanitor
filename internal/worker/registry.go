package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

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
	LastSeen          time.Time
	TokenID           string
}

// Registry tracks connected remote workers.
// Used by the controller in multi-node mode.
type Registry struct {
	workers map[string]*registeredWorker
	mu      sync.RWMutex
	log     *slog.Logger

	// Callbacks fired on registration/unregistration (e.g. StatusManager subscribes)
	onRegister   func(nodeID string, w Worker)
	onUnregister func(nodeID string)
}

type registeredWorker struct {
	worker *RemoteWorker
	info   WorkerInfo
}

func NewRegistry(log *slog.Logger) *Registry {
	return &Registry{
		workers: make(map[string]*registeredWorker),
		log:     log,
	}
}

// SetCallbacks sets the registration/unregistration callbacks.
// Must be called before any workers register.
func (r *Registry) SetCallbacks(onRegister func(string, Worker), onUnregister func(string)) {
	r.onRegister = onRegister
	r.onUnregister = onUnregister
}

func (r *Registry) Register(w *RemoteWorker, info WorkerInfo) {
	r.mu.Lock()

	// Close old connection if re-registering
	if old, ok := r.workers[w.nodeID]; ok {
		old.worker.Close()
	}

	info.LastSeen = time.Now()
	r.workers[w.nodeID] = &registeredWorker{worker: w, info: info}
	r.log.Info("worker registered", "worker_id", w.nodeID, "lan_ip", info.LanIP, "external_ip", info.ExternalIP)
	r.mu.Unlock()

	if r.onRegister != nil {
		r.onRegister(w.nodeID, w)
	}
}

func (r *Registry) Unregister(nodeID string) {
	r.mu.Lock()

	rw, ok := r.workers[nodeID]
	if !ok {
		r.mu.Unlock()
		return
	}

	rw.worker.Close()
	delete(r.workers, nodeID)
	r.log.Info("worker unregistered", "worker_id", nodeID)
	r.mu.Unlock()

	if r.onUnregister != nil {
		r.onUnregister(nodeID)
	}
}

func (r *Registry) Get(nodeID string) (Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rw, ok := r.workers[nodeID]
	if !ok {
		return nil, false
	}
	return rw.worker, true
}

func (r *Registry) GetInfo(nodeID string) (WorkerInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rw, ok := r.workers[nodeID]
	if !ok {
		return WorkerInfo{}, false
	}
	return rw.info, true
}

func (r *Registry) UpdateHeartbeat(nodeID string, info WorkerInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rw, ok := r.workers[nodeID]
	if !ok {
		return fmt.Errorf("worker %s not registered", nodeID)
	}
	info.LastSeen = time.Now()
	info.TokenID = rw.info.TokenID // preserve token from registration
	rw.info = info
	return nil
}

// ListWorkers returns info for all registered workers.
func (r *Registry) ListWorkers() []WorkerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]WorkerInfo, 0, len(r.workers))
	for _, rw := range r.workers {
		infos = append(infos, rw.info)
	}
	return infos
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workers)
}

// StartReaper starts a goroutine that removes workers with stale heartbeats.
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
		if time.Since(rw.info.LastSeen) > timeout {
			stale = append(stale, id)
		}
	}
	r.mu.RUnlock()

	for _, id := range stale {
		log.Warn("worker heartbeat timeout, unregistering", "worker_id", id)
		r.Unregister(id)
	}
}
