package status

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gjq"
)

const (
	queryPollInterval = 5 * time.Second
)

type QueryData struct {
	PlayersOnline int           `json:"players_online"`
	MaxPlayers    int           `json:"max_players"`
	Players       []QueryPlayer `json:"players"`
	Map           string        `json:"map"`
	Version       string        `json:"version"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

type QueryPlayer struct {
	Name string `json:"name"`
}

type QueryService struct {
	store       Store
	log         *slog.Logger
	broadcaster *controller.EventBus
	gameStore   *games.GameStore
	mu          sync.RWMutex
	cache       map[string]*QueryData
	pollers     map[string]context.CancelFunc
}

func NewQueryService(store Store, broadcaster *controller.EventBus, gameStore *games.GameStore, log *slog.Logger) *QueryService {
	return &QueryService{
		store:       store,
		log:         log,
		broadcaster: broadcaster,
		gameStore:   gameStore,
		cache:       make(map[string]*QueryData),
		pollers:     make(map[string]context.CancelFunc),
	}
}

func (s *QueryService) GetQueryData(gameserverID string) *QueryData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache[gameserverID]
}

// StartPolling begins GJQ polling for a gameserver.
// Only collects player/map/version data — does not promote status.
// No-op for games without query support.
func (s *QueryService) StartPolling(gameserverID string) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		s.log.Error("failed to load gameserver for polling", "id", gameserverID, "error", err)
		return
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		s.log.Error("game not found for polling", "id", gameserverID, "game_id", gs.GameID)
		return
	}

	if !s.gameSupportsQuery(game) {
		s.log.Debug("game does not support query, skipping polling", "id", gameserverID)
		return
	}

	hostPort := s.getHostPort(gs)
	if hostPort == 0 {
		s.log.Warn("no host port found for gameserver, skipping polling", "id", gameserverID)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	if oldCancel, exists := s.pollers[gameserverID]; exists {
		oldCancel()
	}
	s.pollers[gameserverID] = cancel
	delete(s.cache, gameserverID)
	s.mu.Unlock()

	slug := game.ID
	if game.GJQSlug != "" {
		slug = game.GJQSlug
	}
	go s.pollLoop(ctx, gameserverID, slug, hostPort)
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

// pollLoop collects query data at a steady interval.
// Does not manage gameserver status — that's ReadyWatcher's job.
func (s *QueryService) pollLoop(ctx context.Context, gameserverID, gameSlug string, port uint16) {
	s.log.Debug("starting GJQ poll loop", "id", gameserverID, "game", gameSlug, "port", port)

	ticker := time.NewTicker(queryPollInterval)
	defer ticker.Stop()

	for {
		gs, err := s.store.GetGameserver(gameserverID)
		if err != nil || gs == nil {
			s.log.Debug("gameserver gone, stopping poll", "id", gameserverID)
			return
		}
		if !controller.IsPollableStatus(gs.Status) {
			s.log.Debug("gameserver not in pollable state, stopping", "id", gameserverID, "status", gs.Status)
			return
		}

		info, err := gjq.Query(ctx, "localhost", port, gjq.QueryOptions{
			Game:    gameSlug,
			Players: true,
			Direct:  true,
			Timeout: 5 * time.Second,
		})

		if err != nil {
			s.log.Debug("GJQ poll failed", "id", gameserverID, "error", err)
		} else {
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

			if changed {
				s.log.Debug("GJQ data changed", "id", gameserverID, "players", info.Players)
				playerNames := make([]string, len(data.Players))
				for i, p := range data.Players {
					playerNames[i] = p.Name
				}
				s.broadcaster.Publish(controller.GameserverQueryEvent{
					GameserverID:  gameserverID,
					PlayersOnline: data.PlayersOnline,
					MaxPlayers:    data.MaxPlayers,
					Players:       playerNames,
					Map:           data.Map,
					Version:       data.Version,
					Timestamp:     time.Now(),
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

func (s *QueryService) gameSupportsQuery(game *games.Game) bool {
	if game.ID == "" {
		return false
	}

	for _, c := range game.DisabledCapabilities {
		if c == controller.CapabilityQuery {
			return false
		}
	}
	return true
}

// flexInt handles JSON values that may be a number or a string containing a number.
type flexInt int

func (fi *flexInt) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*fi = flexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*fi = 0
		return nil
	}
	n64, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*fi = flexInt(n64)
	return nil
}

func (s *QueryService) getHostPort(gs *model.Gameserver) uint16 {
	var ports []struct {
		Name     string  `json:"name"`
		HostPort flexInt `json:"host_port"`
	}
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return 0
	}
	for _, p := range ports {
		if p.Name == controller.PortNameQuery {
			return uint16(p.HostPort)
		}
	}
	for _, p := range ports {
		if p.Name == controller.PortNameGame {
			return uint16(p.HostPort)
		}
	}
	if len(ports) > 0 {
		return uint16(ports[0].HostPort)
	}
	return 0
}
