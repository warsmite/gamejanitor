package handlers

import (
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
)

type StatusHandlers struct {
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	workerSvc     *service.WorkerNodeService
	cfg           config.Config
	log           *slog.Logger
}

func NewStatusHandlers(gameserverSvc *service.GameserverService, querySvc *service.QueryService, workerSvc *service.WorkerNodeService, cfg config.Config, log *slog.Logger) *StatusHandlers {
	return &StatusHandlers{gameserverSvc: gameserverSvc, querySvc: querySvc, workerSvc: workerSvc, cfg: cfg, log: log}
}

type clusterStatus struct {
	Workers            int     `json:"workers"`
	WorkersCordoned    int     `json:"workers_cordoned"`
	TotalMemoryMB      int64   `json:"total_memory_mb"`
	AllocatedMemoryMB  int     `json:"allocated_memory_mb"`
	TotalCPU           float64 `json:"total_cpu"`
	AllocatedCPU       float64 `json:"allocated_cpu"`
	TotalStorageMB     int64   `json:"total_storage_mb"`
	AllocatedStorageMB int     `json:"allocated_storage_mb"`
}

type configStatus struct {
	Bind             string `json:"bind"`
	Port             int    `json:"port"`
	GRPCPort         int    `json:"grpc_port"`
	SFTPPort         int    `json:"sftp_port"`
	DataDir          string `json:"data_dir"`
	ContainerRuntime string `json:"container_runtime"`
	BackupStoreType  string `json:"backup_store_type"`
	WebUI            bool   `json:"web_ui"`
	Controller       bool   `json:"controller"`
	Worker           bool   `json:"worker"`
}

type gameserverStatus struct {
	Total      int `json:"total"`
	Running    int `json:"running"`
	Stopped    int `json:"stopped"`
	Installing int `json:"installing"`
	Error      int `json:"error"`
}

func (h *StatusHandlers) Get(w http.ResponseWriter, r *http.Request) {
	filter := models.GameserverFilter{}
	gameservers, err := h.gameserverSvc.ListGameservers(r.Context(), filter)
	if err != nil {
		h.log.Error("listing gameservers for status", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	gs := gameserverStatus{Total: len(gameservers)}
	for _, g := range gameservers {
		switch g.Status {
		case service.StatusRunning, service.StatusStarted:
			gs.Running++
		case service.StatusStopped:
			gs.Stopped++
		case service.StatusInstalling:
			gs.Installing++
		case service.StatusError:
			gs.Error++
		}
	}

	cluster := h.buildClusterStatus()

	backupStoreType := "local"
	if h.cfg.BackupStore != nil && h.cfg.BackupStore.Type != "" {
		backupStoreType = h.cfg.BackupStore.Type
	}

	respondOK(w, map[string]any{
		"config": configStatus{
			Bind:             h.cfg.Bind,
			Port:             h.cfg.Port,
			GRPCPort:         h.cfg.GRPCPort,
			SFTPPort:         h.cfg.SFTPPort,
			DataDir:          h.cfg.DataDir,
			ContainerRuntime: h.cfg.ContainerRuntime,
			BackupStoreType:  backupStoreType,
			WebUI:            h.cfg.WebUI,
			Controller:       h.cfg.Controller,
			Worker:           h.cfg.Worker,
		},
		"cluster":     cluster,
		"gameservers": gs,
	})
}

func (h *StatusHandlers) buildClusterStatus() clusterStatus {
	var cs clusterStatus

	workers, err := h.workerSvc.List()
	if err != nil {
		h.log.Error("listing workers for cluster status", "error", err)
		return cs
	}

	cs.Workers = len(workers)
	for _, w := range workers {
		cs.TotalMemoryMB += w.MemoryTotalMB
		cs.AllocatedMemoryMB += w.AllocatedMemoryMB
		cs.TotalCPU += float64(w.CPUCores)
		cs.AllocatedCPU += w.AllocatedCPU
		cs.TotalStorageMB += w.DiskTotalMB
		cs.AllocatedStorageMB += w.AllocatedStorageMB
		if w.Cordoned {
			cs.WorkersCordoned++
		}
	}

	return cs
}
