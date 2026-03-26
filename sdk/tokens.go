package gamejanitor

import "context"

// TokenService handles API token management.
type TokenService struct {
	client *Client
}

// List returns all API tokens (hashed values excluded).
func (s *TokenService) List(ctx context.Context) ([]Token, error) {
	var tokens []Token
	if err := s.client.get(ctx, "/api/tokens", &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// Create creates a new API token. The raw token is only returned once.
func (s *TokenService) Create(ctx context.Context, req *CreateTokenRequest) (*CreateTokenResponse, error) {
	var resp CreateTokenResponse
	if err := s.client.post(ctx, "/api/tokens", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Delete deletes an API token.
func (s *TokenService) Delete(ctx context.Context, tokenID string) error {
	return s.client.delete(ctx, "/api/tokens/"+tokenID)
}

// ListWorkerTokens returns all worker tokens.
func (s *TokenService) ListWorkerTokens(ctx context.Context) ([]Token, error) {
	var tokens []Token
	if err := s.client.get(ctx, "/api/worker-tokens", &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// CreateWorkerToken creates a new worker token. The raw token is only returned once.
func (s *TokenService) CreateWorkerToken(ctx context.Context, req *CreateWorkerTokenRequest) (*CreateTokenResponse, error) {
	var resp CreateTokenResponse
	if err := s.client.post(ctx, "/api/worker-tokens", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RotateWorkerToken rotates an existing worker token. The new raw token is returned once.
func (s *TokenService) RotateWorkerToken(ctx context.Context, req *RotateWorkerTokenRequest) (*CreateTokenResponse, error) {
	var resp CreateTokenResponse
	if err := s.client.post(ctx, "/api/worker-tokens/rotate", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteWorkerToken deletes a worker token.
func (s *TokenService) DeleteWorkerToken(ctx context.Context, tokenID string) error {
	return s.client.delete(ctx, "/api/worker-tokens/"+tokenID)
}
