package gamejanitor

import "context"

// TokenService handles API token management.
type TokenService struct {
	client *Client
}

// List returns API tokens (hashed values excluded).
// If a role is provided, only tokens with that role are returned.
func (s *TokenService) List(ctx context.Context, role ...string) ([]Token, error) {
	path := "/api/tokens"
	if len(role) > 0 && role[0] != "" {
		path += "?role=" + role[0]
	}
	var tokens []Token
	if err := s.client.get(ctx, path, &tokens); err != nil {
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

// Rotate rotates an existing token by ID. Only worker tokens support rotation.
// The new raw token is returned once.
func (s *TokenService) Rotate(ctx context.Context, tokenID string) (*CreateTokenResponse, error) {
	var resp CreateTokenResponse
	if err := s.client.post(ctx, "/api/tokens/"+tokenID+"/rotate", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Delete deletes an API token.
func (s *TokenService) Delete(ctx context.Context, tokenID string) error {
	return s.client.delete(ctx, "/api/tokens/"+tokenID)
}
