package service

import (
	"database/sql"
	"log/slog"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

type GameService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewGameService(db *sql.DB, log *slog.Logger) *GameService {
	return &GameService{db: db, log: log}
}

func (s *GameService) ListGames() ([]models.Game, error) {
	return models.ListGames(s.db)
}

func (s *GameService) GetGame(id string) (*models.Game, error) {
	return models.GetGame(s.db, id)
}

func (s *GameService) CreateGame(game *models.Game) error {
	s.log.Info("creating game", "id", game.ID, "name", game.Name)
	return models.CreateGame(s.db, game)
}

func (s *GameService) UpdateGame(game *models.Game) error {
	s.log.Info("updating game", "id", game.ID)
	return models.UpdateGame(s.db, game)
}

func (s *GameService) DeleteGame(id string) error {
	s.log.Info("deleting game", "id", id)
	return models.DeleteGame(s.db, id)
}
