package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

// Store defines the persistence operations needed by the webhook worker.
type Store interface {
	ListEnabledWebhookEndpoints() ([]model.WebhookEndpoint, error)
	GetWebhookEndpoint(id string) (*model.WebhookEndpoint, error)
	ListWebhookEndpoints() ([]model.WebhookEndpoint, error)
	CreateWebhookEndpoint(e *model.WebhookEndpoint) error
	UpdateWebhookEndpoint(e *model.WebhookEndpoint) error
	DeleteWebhookEndpoint(id string) error
	CreateWebhookDelivery(d *model.WebhookDelivery) error
	ListDeliveriesByEndpoint(endpointID string, state string, limit int) ([]model.WebhookDelivery, error)
	GetPendingDeliveries(limit int) ([]model.WebhookDelivery, error)
	MarkDeliverySuccess(id string) error
	MarkDeliveryRetry(id string, nextAttemptAt time.Time, lastError string) error
	MarkDeliveryFailed(id string, lastError string) error
	PruneWebhookDeliveries() (int64, error)
}

// GameserverLookup resolves gameserver data for webhook payloads.
type GameserverLookup interface {
	GetGameserver(id string) (*model.Gameserver, error)
	PopulateNode(gs *model.Gameserver)
}

type WebhookPayload struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Data      any       `json:"data"`
}

// Payload data types -- one per event category.


type WebhookWorker struct {
	store       Store
	gsLookup    GameserverLookup
	broadcaster *event.EventBus
	client      *http.Client
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	wake        chan struct{} // signals deliverLoop to process immediately

	// ValidateURL is called before each webhook delivery. Returns an error if the
	// URL is not allowed (e.g., private IP in restricted mode). Nil means no validation.
	ValidateURL func(string) error

	// PollInterval overrides the default delivery poll interval. Zero uses the default (5s).
	PollInterval time.Duration
}

func NewWebhookWorker(store Store, gsLookup GameserverLookup, broadcaster *event.EventBus, log *slog.Logger) *WebhookWorker {
	return &WebhookWorker{
		store:       store,
		gsLookup:    gsLookup,
		broadcaster: broadcaster,
		client:      &http.Client{Timeout: 10 * time.Second},
		log:         log,
		wake:        make(chan struct{}, 1),
	}
}

func (w *WebhookWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)

	w.wg.Add(2)
	go w.subscribeLoop(ctx)
	go w.deliverLoop(ctx)

	w.log.Info("webhook worker started")
}

func (w *WebhookWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	w.log.Info("webhook worker stopped")
}

func (w *WebhookWorker) subscribeLoop(ctx context.Context) {
	defer w.wg.Done()

	ch, unsubscribe := w.broadcaster.Subscribe()
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			w.enqueueEvent(evt)
		}
	}
}

func (w *WebhookWorker) enqueueEvent(evt event.WebhookEvent) {
	endpoints, err := w.store.ListEnabledWebhookEndpoints()
	if err != nil {
		w.log.Error("webhook: failed to list enabled endpoints", "error", err)
		return
	}
	if len(endpoints) == 0 {
		return
	}

	e, ok := evt.(event.Event)
	if !ok {
		return
	}
	eventType := e.Type

	for _, ep := range endpoints {
		if !matchEventFilter(eventType, []string(ep.Events)) {
			continue
		}

		payload := WebhookPayload{
			Version:   1,
			ID:        uuid.New().String(),
			Timestamp: e.Timestamp,
			EventType: eventType,
			Data:      e,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			w.log.Error("webhook: failed to marshal payload", "event_type", eventType, "error", err)
			continue
		}

		now := time.Now().UTC()
		delivery := &model.WebhookDelivery{
			ID:                payload.ID,
			WebhookEndpointID: ep.ID,
			EventType:         payload.EventType,
			Payload:           string(body),
			NextAttemptAt:     now,
			CreatedAt:         now,
		}
		if err := w.store.CreateWebhookDelivery(delivery); err != nil {
			w.log.Error("webhook: failed to enqueue delivery", "endpoint", ep.ID, "event_type", eventType, "error", err)
		}
	}

	// Wake the delivery goroutine to process immediately instead of waiting for the next poll tick
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// matchEventFilter checks if an event type matches any of the filter patterns.
// Supports "*" for all events and glob patterns like "gameserver.*".
func matchEventFilter(eventType string, patterns []string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if matched, _ := path.Match(p, eventType); matched {
			return true
		}
	}
	return false
}

const (
	maxDeliveryAttempts  = 24   // ~24 hours total with exponential backoff capped at 1 hour
	deliveryPollInterval = 5 * time.Second
	maxBackoffSeconds    = 3600 // 1 hour max between retries
)

func (w *WebhookWorker) deliverLoop(ctx context.Context) {
	defer w.wg.Done()

	interval := w.PollInterval
	if interval == 0 {
		interval = deliveryPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastCleanup := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.processPendingDeliveries()
		case <-ticker.C:
			w.processPendingDeliveries()

			if time.Since(lastCleanup) > time.Hour {
				if pruned, err := w.store.PruneWebhookDeliveries(); err != nil {
					w.log.Error("webhook: failed to prune deliveries", "error", err)
				} else if pruned > 0 {
					w.log.Info("webhook: pruned old deliveries", "count", pruned)
				}
				lastCleanup = time.Now()
			}
		}
	}
}

func (w *WebhookWorker) processPendingDeliveries() {
	deliveries, err := w.store.GetPendingDeliveries(10)
	if err != nil {
		w.log.Error("webhook: failed to fetch pending deliveries", "error", err)
		return
	}

	// Cache endpoint lookups within this poll cycle
	endpointCache := make(map[string]*model.WebhookEndpoint)

	for _, d := range deliveries {
		ep, cached := endpointCache[d.WebhookEndpointID]
		if !cached {
			ep, err = w.store.GetWebhookEndpoint(d.WebhookEndpointID)
			if err != nil {
				w.log.Error("webhook: failed to fetch endpoint for delivery", "id", d.ID, "endpoint", d.WebhookEndpointID, "error", err)
				continue
			}
			endpointCache[d.WebhookEndpointID] = ep
		}

		if ep == nil {
			// Endpoint was deleted but CASCADE didn't clean up (shouldn't happen)
			if err := w.store.MarkDeliveryFailed(d.ID, "endpoint deleted"); err != nil {
				w.log.Error("webhook: failed to mark orphan delivery failed", "id", d.ID, "error", err)
			}
			continue
		}

		statusCode, deliverErr := w.deliver(ep.URL, []byte(d.Payload), ep.Secret)

		if deliverErr == nil && statusCode >= 200 && statusCode < 300 {
			if err := w.store.MarkDeliverySuccess(d.ID); err != nil {
				w.log.Error("webhook: failed to mark delivery success", "id", d.ID, "error", err)
			} else {
				w.log.Info("webhook: delivered", "id", d.ID, "event_type", d.EventType, "endpoint", ep.ID, "response_status", statusCode)
			}
			continue
		}

		errMsg := ""
		if deliverErr != nil {
			errMsg = deliverErr.Error()
		} else {
			errMsg = fmt.Sprintf("HTTP %d", statusCode)
		}

		newAttempts := d.Attempts + 1
		if newAttempts >= maxDeliveryAttempts {
			if err := w.store.MarkDeliveryFailed(d.ID, errMsg); err != nil {
				w.log.Error("webhook: failed to mark delivery failed", "id", d.ID, "error", err)
			}
			w.log.Error("webhook: delivery permanently failed", "id", d.ID, "event_type", d.EventType, "endpoint", ep.ID, "attempts", newAttempts, "last_error", errMsg)
			continue
		}

		backoffSec := 5 * (1 << d.Attempts) // 5, 10, 20, 40, 80, ...
		if backoffSec > maxBackoffSeconds {
			backoffSec = maxBackoffSeconds
		}
		nextAttempt := time.Now().UTC().Add(time.Duration(backoffSec) * time.Second)

		if err := w.store.MarkDeliveryRetry(d.ID, nextAttempt, errMsg); err != nil {
			w.log.Error("webhook: failed to mark delivery for retry", "id", d.ID, "error", err)
		}
		w.log.Warn("webhook: delivery failed, will retry",
			"id", d.ID, "event_type", d.EventType, "endpoint", ep.ID, "attempt", newAttempts,
			"next_attempt", nextAttempt.Format(time.RFC3339), "error", errMsg)
	}
}

func (w *WebhookWorker) deliver(url string, body []byte, secret string) (int, error) {
	if w.ValidateURL != nil {
		if err := w.ValidateURL(url); err != nil {
			return 0, fmt.Errorf("blocked webhook URL: %v", err)
		}
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gamejanitor-Webhook/1.0")

	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Webhook-Signature", "sha256="+sig)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, nil
}
