package gamejanitor

import "context"

// WorkerService handles worker node API calls.
type WorkerService struct {
	client *Client
}

// List returns all worker nodes in the cluster.
func (s *WorkerService) List(ctx context.Context) ([]Worker, error) {
	var workers []Worker
	if err := s.client.get(ctx, "/api/cluster/workers", &workers); err != nil {
		return nil, err
	}
	return workers, nil
}

// Get returns a single worker node.
func (s *WorkerService) Get(ctx context.Context, workerID string) (*Worker, error) {
	var worker Worker
	if err := s.client.get(ctx, "/api/cluster/workers/"+workerID, &worker); err != nil {
		return nil, err
	}
	return &worker, nil
}

// Update partially updates a worker node's configuration.
func (s *WorkerService) Update(ctx context.Context, workerID string, req *UpdateWorkerRequest) (*Worker, error) {
	var worker Worker
	if err := s.client.patch(ctx, "/api/cluster/workers/"+workerID, req, &worker); err != nil {
		return nil, err
	}
	return &worker, nil
}
