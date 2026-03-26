// Package gamejanitor provides a Go client for the Gamejanitor API.
//
// Usage:
//
//	c := gamejanitor.New("https://panel.example.com", gamejanitor.WithToken("gj_..."))
//	gs, err := c.Gameservers.Get(ctx, "some-id")
package gamejanitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// TokenSource provides a token for each request. Implement this for dynamic
// token rotation; for static tokens use [WithToken] instead.
type TokenSource interface {
	Token() string
}

type staticToken string

func (s staticToken) Token() string { return string(s) }

// Client is the Gamejanitor API client. Create one with [New].
type Client struct {
	baseURL     string
	httpClient  *http.Client
	tokenSource TokenSource
	userAgent   string

	Gameservers *GameserverService
	Backups     *BackupService
	Files       *FileService
	Mods        *ModService
	Schedules   *ScheduleService
	Workers     *WorkerService
	Tokens      *TokenService
	Webhooks    *WebhookService
	Events      *EventService
	Activity  *ActivityService
	Settings    *SettingsService
	Games       *GameService
	Status      *StatusService
	Logs        *LogService
}

// Option configures a [Client].
type Option func(*Client)

// WithToken sets a static API token for authentication.
func WithToken(token string) Option {
	return func(c *Client) { c.tokenSource = staticToken(token) }
}

// WithTokenSource sets a dynamic token source for authentication.
// Use this when you need token rotation without rebuilding the client.
func WithTokenSource(ts TokenSource) Option {
	return func(c *Client) { c.tokenSource = ts }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithUserAgent sets a custom User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// New creates a new Gamejanitor API client.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: http.DefaultClient,
		userAgent:  "gamejanitor-go-sdk",
	}
	for _, opt := range opts {
		opt(c)
	}

	c.Gameservers = &GameserverService{client: c}
	c.Backups = &BackupService{client: c}
	c.Files = &FileService{client: c}
	c.Mods = &ModService{client: c}
	c.Schedules = &ScheduleService{client: c}
	c.Workers = &WorkerService{client: c}
	c.Tokens = &TokenService{client: c}
	c.Webhooks = &WebhookService{client: c}
	c.Events = &EventService{client: c}
	c.Activity = &ActivityService{client: c}
	c.Settings = &SettingsService{client: c}
	c.Games = &GameService{client: c}
	c.Status = &StatusService{client: c}
	c.Logs = &LogService{client: c}

	return c
}

// apiResponse is the standard envelope returned by all API endpoints.
type apiResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	u := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("gamejanitor: encoding request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("gamejanitor: creating request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.tokenSource != nil {
		req.Header.Set("Authorization", "Bearer "+c.tokenSource.Token())
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	return req, nil
}

// do executes a request, unwraps the API envelope, and decodes data into dest.
// Pass nil for dest on 204 No Content responses.
func (c *Client) do(req *http.Request, dest any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gamejanitor: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	var envelope apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("gamejanitor: decoding response: %w", err)
	}

	if envelope.Status == "error" || resp.StatusCode >= 400 {
		return &Error{
			StatusCode: resp.StatusCode,
			Message:    envelope.Error,
		}
	}

	if dest != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, dest); err != nil {
			return fmt.Errorf("gamejanitor: decoding response data: %w", err)
		}
	}

	return nil
}

// doRaw executes a request and returns the raw response. Caller must close the body.
// Used for binary downloads (backups, file downloads).
func (c *Client) doRaw(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamejanitor: request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var envelope apiResponse
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return nil, &Error{StatusCode: resp.StatusCode, Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return nil, &Error{StatusCode: resp.StatusCode, Message: envelope.Error}
	}

	return resp, nil
}

// get is a convenience for GET requests that decode JSON into dest.
func (c *Client) get(ctx context.Context, path string, dest any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return c.do(req, dest)
}

// post is a convenience for POST requests.
func (c *Client) post(ctx context.Context, path string, body any, dest any) error {
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	return c.do(req, dest)
}

// patch is a convenience for PATCH requests.
func (c *Client) patch(ctx context.Context, path string, body any, dest any) error {
	req, err := c.newRequest(ctx, http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	return c.do(req, dest)
}

// delete is a convenience for DELETE requests.
func (c *Client) delete(ctx context.Context, path string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// ListOptions configures pagination for list endpoints.
type ListOptions struct {
	Limit  int
	Offset int
}

func (o *ListOptions) encode() string {
	if o == nil {
		return ""
	}
	v := url.Values{}
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
