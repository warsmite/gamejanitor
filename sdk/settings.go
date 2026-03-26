package gamejanitor

import (
	"context"
	"encoding/json"
)

// SettingsService handles cluster settings API calls.
type SettingsService struct {
	client *Client
}

// Get returns all cluster settings.
func (s *SettingsService) Get(ctx context.Context) (Settings, error) {
	var settings Settings
	if err := s.client.get(ctx, "/api/settings", &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// Update modifies cluster settings. Pass a map of setting keys to new values.
// Use an empty string value to clear a setting.
func (s *SettingsService) Update(ctx context.Context, settings map[string]json.RawMessage) error {
	return s.client.patch(ctx, "/api/settings", settings, nil)
}
