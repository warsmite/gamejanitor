package service

import (
	"database/sql"
	"log/slog"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

type WorkerNodeService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewWorkerNodeService(db *sql.DB, log *slog.Logger) *WorkerNodeService {
	return &WorkerNodeService{db: db, log: log}
}

func (s *WorkerNodeService) GetWorkerNode(id string) (*models.WorkerNode, error) {
	return models.GetWorkerNode(s.db, id)
}

func (s *WorkerNodeService) SetWorkerNodePortRange(id string, start, end *int) error {
	return models.SetWorkerNodePortRange(s.db, id, start, end)
}

func (s *WorkerNodeService) SetWorkerNodeCordoned(id string, cordoned bool) error {
	return models.SetWorkerNodeCordoned(s.db, id, cordoned)
}

func (s *WorkerNodeService) SetWorkerNodeLimits(id string, maxMemoryMB *int, maxCPU *float64, maxStorageMB *int) error {
	return models.SetWorkerNodeLimits(s.db, id, maxMemoryMB, maxCPU, maxStorageMB)
}

func (s *WorkerNodeService) ListGameserversByNode() ([]models.Gameserver, error) {
	return models.ListGameservers(s.db, models.GameserverFilter{})
}
