package service

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
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/internal/models"
)

// WebhookEndpointService manages webhook endpoint CRUD and test delivery.
// Separate from WebhookWorker which handles async event→delivery.
type WebhookEndpointService struct {
	db     *sql.DB
	client *http.Client
	log    *slog.Logger
}

func NewWebhookEndpointService(db *sql.DB, log *slog.Logger) *WebhookEndpointService {
	return &WebhookEndpointService{
		db:     db,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// WebhookEndpointView is the API representation of a webhook endpoint.
// Hides the raw secret — only indicates whether one is set.
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

func toEndpointView(e *models.WebhookEndpoint) WebhookEndpointView {
	var events []string
	if err := json.Unmarshal([]byte(e.Events), &events); err != nil {
		events = []string{}
	}
	return WebhookEndpointView{
		ID:          e.ID,
		Description: e.Description,
		URL:         e.URL,
		SecretSet:   e.Secret != "",
		Events:      events,
		Enabled:     e.Enabled,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}
}

func (s *WebhookEndpointService) List() ([]WebhookEndpointView, error) {
	endpoints, err := models.ListWebhookEndpoints(s.db)
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
	ep, err := models.GetWebhookEndpoint(s.db, id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, ErrNotFoundf("webhook endpoint %s not found", id)
	}
	v := toEndpointView(ep)
	return &v, nil
}

func (s *WebhookEndpointService) Create(url, description, secret string, events []string, enabled bool) (*WebhookEndpointView, error) {
	if url == "" {
		return nil, ErrBadRequest("url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, ErrBadRequest("url must start with http:// or https://")
	}

	if len(events) == 0 {
		events = []string{"*"}
	}
	if err := ValidateEventFilter(events); err != nil {
		return nil, err
	}

	eventsJSON, _ := json.Marshal(events)
	ep := &models.WebhookEndpoint{
		Description: description,
		URL:         url,
		Secret:      secret,
		Events:      string(eventsJSON),
		Enabled:     enabled,
	}
	if err := models.CreateWebhookEndpoint(s.db, ep); err != nil {
		return nil, err
	}

	s.log.Info("webhook endpoint created", "id", ep.ID, "url", ep.URL)
	v := toEndpointView(ep)
	return &v, nil
}

func (s *WebhookEndpointService) Update(id string, description, url, secret *string, events []string, enabled *bool) (*WebhookEndpointView, error) {
	ep, err := models.GetWebhookEndpoint(s.db, id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, ErrNotFoundf("webhook endpoint %s not found", id)
	}

	if description != nil {
		ep.Description = *description
	}
	if url != nil {
		if !strings.HasPrefix(*url, "http://") && !strings.HasPrefix(*url, "https://") {
			return nil, ErrBadRequest("url must start with http:// or https://")
		}
		ep.URL = *url
	}
	if secret != nil {
		ep.Secret = *secret
	}
	if events != nil {
		if err := ValidateEventFilter(events); err != nil {
			return nil, err
		}
		eventsJSON, _ := json.Marshal(events)
		ep.Events = string(eventsJSON)
	}
	if enabled != nil {
		ep.Enabled = *enabled
	}

	if err := models.UpdateWebhookEndpoint(s.db, ep); err != nil {
		return nil, err
	}

	s.log.Info("webhook endpoint updated", "id", ep.ID)
	v := toEndpointView(ep)
	return &v, nil
}

func (s *WebhookEndpointService) Delete(id string) error {
	if err := models.DeleteWebhookEndpoint(s.db, id); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFoundf("webhook endpoint %s not found", id)
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
	ep, err := models.GetWebhookEndpoint(s.db, endpointID)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, ErrNotFoundf("webhook endpoint %s not found", endpointID)
	}

	deliveries, err := models.ListDeliveriesByEndpoint(s.db, endpointID, state, limit)
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
	ep, err := models.GetWebhookEndpoint(s.db, id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, ErrNotFoundf("webhook endpoint %s not found", id)
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
		return ErrBadRequest("events must not be empty")
	}
	for _, e := range events {
		if e == "*" {
			continue
		}
		matched := false
		for _, known := range AllEventTypes {
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
			return ErrBadRequestf("event filter %q does not match any known event types", e)
		}
	}
	return nil
}
