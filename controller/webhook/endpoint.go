package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

// WebhookEndpointService manages webhook endpoint CRUD and test delivery.
// Separate from WebhookWorker which handles async event->delivery.
type WebhookEndpointService struct {
	store  Store
	client *http.Client
	log    *slog.Logger
}

func NewWebhookEndpointService(store Store, log *slog.Logger) *WebhookEndpointService {
	return &WebhookEndpointService{
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// WebhookEndpointView is the API representation of a webhook endpoint.
// Hides the raw secret -- only indicates whether one is set.
type WebhookEndpointView struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	SecretSet   bool      `json:"secret_set"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toEndpointView(e *model.WebhookEndpoint) WebhookEndpointView {
	return WebhookEndpointView{
		ID:          e.ID,
		Description: e.Description,
		URL:         e.URL,
		SecretSet:   e.Secret != "",
		Events:      []string(e.Events),
		Enabled:     e.Enabled,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}
}

func (s *WebhookEndpointService) List() ([]WebhookEndpointView, error) {
	endpoints, err := s.store.ListWebhookEndpoints()
	if err != nil {
		return nil, err
	}
	views := make([]WebhookEndpointView, 0, len(endpoints))
	for _, e := range endpoints {
		views = append(views, toEndpointView(&e))
	}
	return views, nil
}

func (s *WebhookEndpointService) Get(id string) (*WebhookEndpointView, error) {
	ep, err := s.store.GetWebhookEndpoint(id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, controller.ErrNotFoundf("webhook endpoint %s not found", id)
	}
	v := toEndpointView(ep)
	return &v, nil
}

type CreateEndpointResult struct {
	Endpoint WebhookEndpointView `json:"endpoint"`
	Warning  string              `json:"warning,omitempty"`
}

func (s *WebhookEndpointService) Create(rawURL, description, secret string, events []string, enabled bool) (*CreateEndpointResult, error) {
	ep := &model.WebhookEndpoint{
		Description: description,
		URL:         rawURL,
		Secret:      secret,
		Enabled:     enabled,
	}
	if err := ep.Validate(); err != nil {
		return nil, err
	}

	if len(events) == 0 {
		events = []string{"*"}
	}
	if err := ValidateEventFilter(events); err != nil {
		return nil, err
	}

	ep.Events = model.StringSlice(events)
	if err := s.store.CreateWebhookEndpoint(ep); err != nil {
		return nil, err
	}

	s.log.Info("webhook endpoint created", "id", ep.ID, "url", ep.URL)

	result := &CreateEndpointResult{Endpoint: toEndpointView(ep)}
	if warning := checkURLReachability(rawURL); warning != "" {
		result.Warning = warning
		s.log.Warn("webhook endpoint created but URL may be unreachable", "id", ep.ID, "url", ep.URL, "warning", warning)
	}

	return result, nil
}

// checkURLReachability does a TCP dial to the URL's host:port to catch obvious misconfigurations.
func checkURLReachability(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Sprintf("could not parse URL: %s", err)
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return fmt.Sprintf("URL is not reachable: %s", err)
	}
	conn.Close()
	return ""
}

func (s *WebhookEndpointService) Update(id string, description, url, secret *string, events []string, enabled *bool) (*WebhookEndpointView, error) {
	ep, err := s.store.GetWebhookEndpoint(id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, controller.ErrNotFoundf("webhook endpoint %s not found", id)
	}

	if description != nil {
		ep.Description = *description
	}
	if url != nil {
		ep.URL = *url
	}
	if secret != nil {
		ep.Secret = *secret
	}
	if events != nil {
		if err := ValidateEventFilter(events); err != nil {
			return nil, err
		}
		ep.Events = model.StringSlice(events)
	}
	if enabled != nil {
		ep.Enabled = *enabled
	}

	if err := ep.Validate(); err != nil {
		return nil, err
	}

	if err := s.store.UpdateWebhookEndpoint(ep); err != nil {
		return nil, err
	}

	s.log.Info("webhook endpoint updated", "id", ep.ID)
	v := toEndpointView(ep)
	return &v, nil
}

func (s *WebhookEndpointService) Delete(id string) error {
	if err := s.store.DeleteWebhookEndpoint(id); err != nil {
		if err == sql.ErrNoRows {
			return controller.ErrNotFoundf("webhook endpoint %s not found", id)
		}
		return err
	}
	s.log.Info("webhook endpoint deleted", "id", id)
	return nil
}

type DeliveryView struct {
	ID            string     `json:"id"`
	EventType     string     `json:"event_type"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (s *WebhookEndpointService) ListDeliveries(endpointID, state string, limit int) ([]DeliveryView, error) {
	// Verify endpoint exists
	ep, err := s.store.GetWebhookEndpoint(endpointID)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, controller.ErrNotFoundf("webhook endpoint %s not found", endpointID)
	}

	deliveries, err := s.store.ListDeliveriesByEndpoint(endpointID, state, limit)
	if err != nil {
		return nil, err
	}

	views := make([]DeliveryView, 0, len(deliveries))
	for _, d := range deliveries {
		views = append(views, DeliveryView{
			ID:            d.ID,
			EventType:     d.EventType,
			State:         d.State,
			Attempts:      d.Attempts,
			LastAttemptAt: d.LastAttemptAt,
			NextAttemptAt: d.NextAttemptAt,
			LastError:     d.LastError,
			CreatedAt:     d.CreatedAt,
		})
	}
	return views, nil
}

type TestResult struct {
	ResponseStatus int  `json:"response_status"`
	Success        bool `json:"success"`
}

func (s *WebhookEndpointService) Test(id string) (*TestResult, error) {
	ep, err := s.store.GetWebhookEndpoint(id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, controller.ErrNotFoundf("webhook endpoint %s not found", id)
	}

	payload := WebhookPayload{
		Version:   1,
		ID:        "test",
		Timestamp: time.Now().UTC(),
		EventType: "webhook.test",
		Data:      map[string]string{},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling test payload: %w", err)
	}

	statusCode, deliverErr := s.deliver(ep.URL, body, ep.Secret)
	if deliverErr != nil {
		return nil, deliverErr
	}

	return &TestResult{
		ResponseStatus: statusCode,
		Success:        statusCode >= 200 && statusCode < 300,
	}, nil
}

func (s *WebhookEndpointService) deliver(url string, body []byte, secret string) (int, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gamejanitor-Webhook/1.0")

	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Webhook-Signature", "sha256="+sig)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, nil
}

// ValidateEventFilter checks that all event filter patterns match at least one known event type.
func ValidateEventFilter(events []string) error {
	if len(events) == 0 {
		return controller.ErrBadRequest("events must not be empty")
	}
	for _, e := range events {
		if e == "*" {
			continue
		}
		matched := false
		for _, known := range event.AllEventTypes {
			if e == known {
				matched = true
				break
			}
			if m, _ := path.Match(e, known); m {
				matched = true
				break
			}
		}
		if !matched {
			return controller.ErrBadRequestf("event filter %q does not match any known event types", e)
		}
	}
	return nil
}
