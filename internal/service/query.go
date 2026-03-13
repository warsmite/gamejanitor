package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gsq"
)

const (
	// queryPollInterval is how often GSQ polls each gameserver for status.
	queryPollInterval = 5 * time.Second
	// queryMaxConsecutiveFails is how many poll failures before setting error status.
	queryMaxConsecutiveFails = 5
)

type QueryData struct {
	PlayersOnline int          `json:"players_online"`
	MaxPlayers    int          `json:"max_players"`
	Players       []QueryPlayer `json:"players"`
	Map           string       `json:"map"`
	Version       string       `json:"version"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type QueryPlayer struct {
	Name string `json:"name"`
}

type QueryService struct {
	db          *sql.DB
	log         *slog.Logger
	broadcaster *EventBroadcaster
	mu          sync.RWMutex
	cache       map[string]*QueryData
	pollers     map[string]context.CancelFunc
}

func NewQueryService(db *sql.DB, broadcaster *EventBroadcaster, log *slog.Logger) *QueryService {
	return &QueryService{
		db:          db,
		log:         log,
		broadcaster: broadcaster,
		cache:       make(map[string]*QueryData),
		pollers:     make(map[string]context.CancelFunc),
	}
}

func (s *QueryService) GetQueryData(gameserverID string) *QueryData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache[gameserverID]
}

// StartPolling begins GSQ polling for a gameserver.
// For games without query support, immediately promotes started → running.
func (s *QueryService) StartPolling(gameserverID string) {
	// DB lookups outside lock
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil || gs == nil {
		s.log.Error("failed to load gameserver for polling", "id", gameserverID, "error", err)
		return
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil || game == nil {
		s.log.Error("failed to load game for polling", "id", gameserverID, "game_id", gs.GameID, "error", err)
		return
	}

	if !s.gameSupportsQuery(game) {
		if gs.Status == StatusStarted {
			s.log.Info("game does not support query, promoting to running", "id", gameserverID)
			setGameserverStatus(s.db, s.log, s.broadcaster, gameserverID, StatusRunning)
		}
		return
	}

	hostPort := s.getHostPort(gs)
	if hostPort == 0 {
		s.log.Warn("no host port found for gameserver, promoting immediately", "id", gameserverID)
		if gs.Status == StatusStarted {
			setGameserverStatus(s.db, s.log, s.broadcaster, gameserverID, StatusRunning)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Hold lock through cancel + register to prevent TOCTOU race
	s.mu.Lock()
	if oldCancel, exists := s.pollers[gameserverID]; exists {
		oldCancel()
	}
	s.pollers[gameserverID] = cancel
	s.mu.Unlock()

	go s.pollLoop(ctx, gameserverID, *game.GSQGameSlug, hostPort)
}

func (s *QueryService) StopPolling(gameserverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, exists := s.pollers[gameserverID]; exists {
		cancel()
		delete(s.pollers, gameserverID)
	}
	delete(s.cache, gameserverID)
}

func (s *QueryService) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, cancel := range s.pollers {
		cancel()
		delete(s.pollers, id)
	}
	s.cache = make(map[string]*QueryData)
	s.log.Info("all query pollers stopped")
}

func (s *QueryService) pollLoop(ctx context.Context, gameserverID, gameSlug string, port uint16) {
	s.log.Debug("starting GSQ poll loop", "id", gameserverID, "game", gameSlug, "port", port)

	consecutiveFailures := 0
	promoted := false

	ticker := time.NewTicker(queryPollInterval)
	defer ticker.Stop()

	for {
		// Check if we should be polling this gameserver
		gs, err := models.GetGameserver(s.db, gameserverID)
		if err != nil || gs == nil {
			s.log.Debug("gameserver gone, stopping poll", "id", gameserverID)
			return
		}
		if gs.Status != StatusStarted && gs.Status != StatusRunning {
			s.log.Debug("gameserver not in pollable state, stopping", "id", gameserverID, "status", gs.Status)
			return
		}

		info, err := gsq.Query(ctx, "localhost", port, gsq.QueryOptions{
			Game:    gameSlug,
			Players: true,
			Timeout: 5 * time.Second,
		})

		if err != nil {
			consecutiveFailures++
			s.log.Debug("GSQ poll failed", "id", gameserverID, "failures", consecutiveFailures, "error", err)

			if promoted && consecutiveFailures >= queryMaxConsecutiveFails {
				s.log.Warn("GSQ poll exceeded max consecutive failures, setting error", "id", gameserverID, "failures", queryMaxConsecutiveFails)
				setGameserverStatus(s.db, s.log, s.broadcaster, gameserverID, StatusError)
				return
			}
		} else {
			consecutiveFailures = 0

			data := &QueryData{
				PlayersOnline: info.Players,
				MaxPlayers:    info.MaxPlayers,
				Map:           info.Map,
				Version:       info.Version,
				UpdatedAt:     time.Now(),
			}
			for _, p := range info.PlayerList {
				data.Players = append(data.Players, QueryPlayer{Name: p.Name})
			}

			s.mu.Lock()
			prev := s.cache[gameserverID]
			changed := prev == nil || prev.PlayersOnline != data.PlayersOnline || prev.MaxPlayers != data.MaxPlayers || len(prev.Players) != len(data.Players)
			s.cache[gameserverID] = data
			s.mu.Unlock()

			if !promoted {
				s.log.Info("GSQ query succeeded, promoting to running", "id", gameserverID, "players", info.Players, "max", info.MaxPlayers)
				setGameserverStatus(s.db, s.log, s.broadcaster, gameserverID, StatusRunning)
				promoted = true
			} else if changed {
				s.log.Debug("GSQ data changed, notifying", "id", gameserverID, "players", info.Players)
				s.broadcaster.PublishStatus(StatusEvent{
					GameserverID: gameserverID,
					OldStatus:    StatusRunning,
					NewStatus:    StatusRunning,
					Timestamp:    time.Now(),
				})
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *QueryService) gameSupportsQuery(game *models.Game) bool {
	if game.GSQGameSlug == nil || *game.GSQGameSlug == "" {
		return false
	}

	var caps []string
	if err := json.Unmarshal(game.DisabledCapabilities, &caps); err != nil {
		return true
	}
	for _, c := range caps {
		if c == "query" {
			return false
		}
	}
	return true
}

func (s *QueryService) getHostPort(gs *models.Gameserver) uint16 {
	var ports []struct {
		Name     string `json:"name"`
		HostPort int    `json:"host_port"`
	}
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return 0
	}
	// Prefer dedicated "query" port, fall back to "game" port
	for _, p := range ports {
		if p.Name == "query" {
			return uint16(p.HostPort)
		}
	}
	for _, p := range ports {
		if p.Name == "game" {
			return uint16(p.HostPort)
		}
	}
	// Fallback to first port
	if len(ports) > 0 {
		return uint16(ports[0].HostPort)
	}
	return 0
}
