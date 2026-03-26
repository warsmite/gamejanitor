package gamejanitor

import (
	"context"
	"encoding/json"
)

// GameService handles game metadata API calls (available game types).
type GameService struct {
	client *Client
}

// List returns all available games.
func (s *GameService) List(ctx context.Context) ([]Game, error) {
	var games []Game
	if err := s.client.get(ctx, "/api/games", &games); err != nil {
		return nil, err
	}
	return games, nil
}

// Get returns a single game definition by ID.
func (s *GameService) Get(ctx context.Context, gameID string) (*Game, error) {
	var game Game
	if err := s.client.get(ctx, "/api/games/"+gameID, &game); err != nil {
		return nil, err
	}
	return &game, nil
}

// Options returns the dynamic options for a game's env var key.
func (s *GameService) Options(ctx context.Context, gameID, key string) (json.RawMessage, error) {
	var options json.RawMessage
	if err := s.client.get(ctx, "/api/games/"+gameID+"/options/"+key, &options); err != nil {
		return nil, err
	}
	return options, nil
}
