package gamejanitor

import (
	"context"
	"fmt"
)

// LogService handles application log API calls.
type LogService struct {
	client *Client
}

// Get returns application logs (not gameserver logs — see [GameserverService.Logs]).
func (s *LogService) Get(ctx context.Context, tail int) ([]string, error) {
	path := "/api/logs"
	if tail > 0 {
		path += fmt.Sprintf("?tail=%d", tail)
	}
	var resp AppLogs
	if err := s.client.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.Lines, nil
}
