package gamejanitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// WebhookService handles webhook endpoint API calls.
type WebhookService struct {
	client *Client
}

// List returns all webhook endpoints.
func (s *WebhookService) List(ctx context.Context) ([]WebhookEndpoint, error) {
	var endpoints []WebhookEndpoint
	if err := s.client.get(ctx, "/api/webhooks", &endpoints); err != nil {
		return nil, err
	}
	return endpoints, nil
}

// Get returns a single webhook endpoint.
func (s *WebhookService) Get(ctx context.Context, webhookID string) (*WebhookEndpoint, error) {
	var endpoint WebhookEndpoint
	if err := s.client.get(ctx, "/api/webhooks/"+webhookID, &endpoint); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// Create creates a new webhook endpoint.
func (s *WebhookService) Create(ctx context.Context, req *CreateWebhookRequest) (*WebhookEndpoint, error) {
	var endpoint WebhookEndpoint
	if err := s.client.post(ctx, "/api/webhooks", req, &endpoint); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// Update partially updates a webhook endpoint.
func (s *WebhookService) Update(ctx context.Context, webhookID string, req *UpdateWebhookRequest) (*WebhookEndpoint, error) {
	var endpoint WebhookEndpoint
	if err := s.client.patch(ctx, "/api/webhooks/"+webhookID, req, &endpoint); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// Delete deletes a webhook endpoint.
func (s *WebhookService) Delete(ctx context.Context, webhookID string) error {
	return s.client.delete(ctx, "/api/webhooks/"+webhookID)
}

// Test sends a test event to a webhook endpoint.
func (s *WebhookService) Test(ctx context.Context, webhookID string) (json.RawMessage, error) {
	var result json.RawMessage
	if err := s.client.post(ctx, "/api/webhooks/"+webhookID+"/test", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// DeliveryListOptions configures filters for listing webhook deliveries.
type DeliveryListOptions struct {
	State string
	Limit int
}

// Deliveries returns delivery history for a webhook endpoint.
func (s *WebhookService) Deliveries(ctx context.Context, webhookID string, opts *DeliveryListOptions) ([]WebhookDelivery, error) {
	v := url.Values{}
	if opts != nil {
		if opts.State != "" {
			v.Set("state", opts.State)
		}
		if opts.Limit > 0 {
			v.Set("limit", fmt.Sprintf("%d", opts.Limit))
		}
	}
	path := "/api/webhooks/" + webhookID + "/deliveries"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}

	var deliveries []WebhookDelivery
	if err := s.client.get(ctx, path, &deliveries); err != nil {
		return nil, err
	}
	return deliveries, nil
}
