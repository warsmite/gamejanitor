package gamejanitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ModService handles mod management API calls for gameservers.
type ModService struct {
	client *Client
}

// List returns all installed mods for a gameserver.
func (s *ModService) List(ctx context.Context, gameserverID string) ([]InstalledMod, error) {
	var mods []InstalledMod
	if err := s.client.get(ctx, "/api/gameservers/"+gameserverID+"/mods", &mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// Sources returns available mod sources for this game.
func (s *ModService) Sources(ctx context.Context, gameserverID string) ([]ModSource, error) {
	var sources []ModSource
	if err := s.client.get(ctx, "/api/gameservers/"+gameserverID+"/mods/sources", &sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// Search searches for mods from a specific source.
func (s *ModService) Search(ctx context.Context, gameserverID string, opts *ModSearchOptions) (json.RawMessage, error) {
	v := url.Values{}
	v.Set("source", opts.Source)
	if opts.Query != "" {
		v.Set("q", opts.Query)
	}
	if opts.Limit > 0 {
		v.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Offset > 0 {
		v.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}

	path := "/api/gameservers/" + gameserverID + "/mods/search?" + v.Encode()
	var results json.RawMessage
	if err := s.client.get(ctx, path, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// ModSearchOptions configures a mod search query.
type ModSearchOptions struct {
	Source string // required
	Query  string
	Limit  int
	Offset int
}

// Versions returns available versions of a mod.
func (s *ModService) Versions(ctx context.Context, gameserverID, source, sourceID string) (json.RawMessage, error) {
	v := url.Values{}
	v.Set("source", source)
	v.Set("source_id", sourceID)
	path := "/api/gameservers/" + gameserverID + "/mods/versions?" + v.Encode()

	var versions json.RawMessage
	if err := s.client.get(ctx, path, &versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// Install installs a mod on a gameserver.
func (s *ModService) Install(ctx context.Context, gameserverID string, req *InstallModRequest) (*InstalledMod, error) {
	var mod InstalledMod
	if err := s.client.post(ctx, "/api/gameservers/"+gameserverID+"/mods", req, &mod); err != nil {
		return nil, err
	}
	return &mod, nil
}

// Uninstall removes a mod from a gameserver.
func (s *ModService) Uninstall(ctx context.Context, gameserverID, modID string) error {
	return s.client.delete(ctx, "/api/gameservers/"+gameserverID+"/mods/"+modID)
}
