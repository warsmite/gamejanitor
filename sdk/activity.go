package gamejanitor

import (
	"context"
	"net/url"
)

// ActivityService handles activity-related API calls.
// Kept as ActivityService for backward compatibility in the SDK.
type ActivityService struct {
	client *Client
}

// ActivityListOptions configures filters for listing activities.
type ActivityListOptions struct {
	GameserverID string
	Type         string
	Status       string // running, completed, failed, abandoned
	WorkerID     string
}

// List returns activities matching the given filters.
func (s *ActivityService) List(ctx context.Context, opts *ActivityListOptions) ([]Activity, error) {
	v := url.Values{}
	if opts != nil {
		if opts.GameserverID != "" {
			v.Set("gameserver_id", opts.GameserverID)
		}
		if opts.Type != "" {
			v.Set("type", opts.Type)
		}
		if opts.Status != "" {
			v.Set("status", opts.Status)
		}
		if opts.WorkerID != "" {
			v.Set("worker_id", opts.WorkerID)
		}
	}
	path := "/api/activity"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}

	var activities []Activity
	if err := s.client.get(ctx, path, &activities); err != nil {
		return nil, err
	}
	return activities, nil
}

// ListByGameserver is a convenience method to list activities for a specific gameserver.
func (s *ActivityService) ListByGameserver(ctx context.Context, gameserverID string) ([]Activity, error) {
	return s.List(ctx, &ActivityListOptions{GameserverID: gameserverID})
}

// Running returns all currently running activities.
func (s *ActivityService) Running(ctx context.Context) ([]Activity, error) {
	return s.List(ctx, &ActivityListOptions{Status: "running"})
}
