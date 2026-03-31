package gamejanitor

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// GameserverService handles gameserver-related API calls.
type GameserverService struct {
	client *Client
}

// List returns all gameservers, filtered by optional parameters.
func (s *GameserverService) List(ctx context.Context, opts *GameserverListOptions) (*GameserverListResponse, error) {
	path := "/api/gameservers" + opts.encode()
	var resp GameserverListResponse
	if err := s.client.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GameserverListOptions configures filters for listing gameservers.
type GameserverListOptions struct {
	Game   string
	Status string
	IDs    []string
	Limit  int
	Offset int
}

func (o *GameserverListOptions) encode() string {
	if o == nil {
		return ""
	}
	v := url.Values{}
	if o.Game != "" {
		v.Set("game", o.Game)
	}
	if o.Status != "" {
		v.Set("status", o.Status)
	}
	if len(o.IDs) > 0 {
		v.Set("ids", strings.Join(o.IDs, ","))
	}
	if o.Limit > 0 {
		v.Set("limit", fmt.Sprintf("%d", o.Limit))
	}
	if o.Offset > 0 {
		v.Set("offset", fmt.Sprintf("%d", o.Offset))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// Get returns a single gameserver by ID.
func (s *GameserverService) Get(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.get(ctx, "/api/gameservers/"+id, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Create creates a new gameserver. The response includes the one-time SFTP password.
func (s *GameserverService) Create(ctx context.Context, req *CreateGameserverRequest) (*CreateGameserverResponse, error) {
	var resp CreateGameserverResponse
	if err := s.client.post(ctx, "/api/gameservers", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Update partially updates a gameserver's configuration.
func (s *GameserverService) Update(ctx context.Context, id string, req *UpdateGameserverRequest) (*UpdateGameserverResponse, error) {
	var resp UpdateGameserverResponse
	if err := s.client.patch(ctx, "/api/gameservers/"+id, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Delete deletes a gameserver. This is an async operation.
func (s *GameserverService) Delete(ctx context.Context, id string) error {
	return s.client.delete(ctx, "/api/gameservers/"+id)
}

// Start starts a gameserver.
func (s *GameserverService) Start(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/start", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Stop stops a gameserver.
func (s *GameserverService) Stop(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/stop", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Restart restarts a gameserver.
func (s *GameserverService) Restart(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/restart", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// UpdateGame triggers a game update/reinstall on the gameserver.
func (s *GameserverService) UpdateGame(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/update-game", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Reinstall reinstalls the game on the gameserver.
func (s *GameserverService) Reinstall(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/reinstall", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Archive stops and archives a gameserver to storage, freeing worker resources.
func (s *GameserverService) Archive(ctx context.Context, id string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/archive", nil, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Unarchive restores an archived gameserver. If nodeID is empty, placement is automatic.
func (s *GameserverService) Unarchive(ctx context.Context, id string, nodeID string) (*Gameserver, error) {
	var body any
	if nodeID != "" {
		body = map[string]string{"node_id": nodeID}
	}
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/unarchive", body, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// Migrate moves a gameserver to a different worker node.
func (s *GameserverService) Migrate(ctx context.Context, id string, nodeID string) (*Gameserver, error) {
	var gs Gameserver
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/migrate", &MigrateRequest{NodeID: nodeID}, &gs); err != nil {
		return nil, err
	}
	return &gs, nil
}

// BulkAction performs a lifecycle action on multiple gameservers.
func (s *GameserverService) BulkAction(ctx context.Context, req *BulkActionRequest) ([]BulkActionResult, error) {
	var results []BulkActionResult
	if err := s.client.post(ctx, "/api/gameservers/bulk", req, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// RegenerateSFTPPassword generates a new SFTP password for the gameserver.
func (s *GameserverService) RegenerateSFTPPassword(ctx context.Context, id string) (*RegenerateSFTPPasswordResponse, error) {
	var resp RegenerateSFTPPasswordResponse
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/regenerate-sftp-password", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Status returns the gameserver's current status and container info.
func (s *GameserverService) Status(ctx context.Context, id string) (*GameserverStatus, error) {
	var resp GameserverStatus
	if err := s.client.get(ctx, "/api/gameservers/"+id+"/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Query returns live query data (players, map, version) for a running gameserver.
func (s *GameserverService) Query(ctx context.Context, id string) (*QueryData, error) {
	var resp QueryData
	if err := s.client.get(ctx, "/api/gameservers/"+id+"/query", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Stats returns resource usage stats for a gameserver.
func (s *GameserverService) Stats(ctx context.Context, id string) (*GameserverStats, error) {
	var resp GameserverStats
	if err := s.client.get(ctx, "/api/gameservers/"+id+"/stats", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Logs returns recent log output from a gameserver.
func (s *GameserverService) Logs(ctx context.Context, id string, tail int) (*LogsResponse, error) {
	path := "/api/gameservers/" + id + "/logs"
	if tail > 0 {
		path += fmt.Sprintf("?tail=%d", tail)
	}
	var resp LogsResponse
	if err := s.client.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SendCommand sends a console command to a running gameserver.
func (s *GameserverService) SendCommand(ctx context.Context, id string, command string) (*SendCommandResponse, error) {
	var resp SendCommandResponse
	if err := s.client.post(ctx, "/api/gameservers/"+id+"/command", &SendCommandRequest{Command: command}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
