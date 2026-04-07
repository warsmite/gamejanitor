package gamejanitor

import "context"

// ClusterService handles cluster-level API calls.
type ClusterService struct {
	client *Client
}

// Get returns the cluster resource summary.
func (s *ClusterService) Get(ctx context.Context) (*ClusterStatusResponse, error) {
	var resp ClusterStatusResponse
	if err := s.client.get(ctx, "/api/cluster", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
