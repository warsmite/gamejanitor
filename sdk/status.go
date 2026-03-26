package gamejanitor

import "context"

// StatusService handles cluster status API calls.
type StatusService struct {
	client *Client
}

// Get returns the cluster status including config, resource allocation, and gameserver counts.
func (s *StatusService) Get(ctx context.Context) (*ClusterStatusResponse, error) {
	var resp ClusterStatusResponse
	if err := s.client.get(ctx, "/api/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
