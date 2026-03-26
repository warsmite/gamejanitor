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
	"github.com/warsmite/gamejanitor/controller"
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
	GetWorkerNode(id string) (*model.WorkerNode, error)
}

type WebhookPayload struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Data      any       `json:"data"`
}

// Payload data types -- one per event category.

type statusChangedData struct {
	GameserverID string           `json:"gameserver_id"`
	OldStatus    string           `json:"old_status"`
	NewStatus    string           `json:"new_status"`
	ErrorReason  string           `json:"error_reason,omitempty"`
	Gameserver   *model.Gameserver `json:"gameserver,omitempty"`
}

type WebhookWorker struct {
	store       Store
	gsLookup    GameserverLookup
	broadcaster *controller.EventBus
	client      *http.Client
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewWebhookWorker(store Store, gsLookup GameserverLookup, broadcaster *controller.EventBus, log *slog.Logger) *WebhookWorker {
	return &WebhookWorker{
		store:       store,
		gsLookup:    gsLookup,
		broadcaster: broadcaster,
		client:      &http.Client{Timeout: 10 * time.Second},
		log:         log,
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
		case event, ok := <-ch:
			if !ok {
				return
			}
			w.enqueueEvent(event)
		}
	}
}

func (w *WebhookWorker) enqueueEvent(event controller.WebhookEvent) {
	// Skip non-transition status events (e.g., query data refresh sends running->running)
	if se, ok := event.(controller.StatusEvent); ok && se.OldStatus == se.NewStatus {
		return
	}

	endpoints, err := w.store.ListEnabledWebhookEndpoints()
	if err != nil {
		w.log.Error("webhook: failed to list enabled endpoints", "error", err)
		return
	}
	if len(endpoints) == 0 {
		return
	}

	// Build payload data -- action events carry their own resource state,
	// lifecycle events are lightweight with just IDs.
	var payloadData any
	switch ev := event.(type) {
	case controller.StatusEvent:
		gs, _ := w.gsLookup.GetGameserver(ev.GameserverID)
		if gs != nil {
			populateNode(w.gsLookup, gs)
		}
		payloadData = statusChangedData{
			GameserverID: ev.GameserverID,
			OldStatus:    ev.OldStatus,
			NewStatus:    ev.NewStatus,
			ErrorReason:  ev.ErrorReason,
			Gameserver:   gs,
		}

	// Action events -- self-contained with full resource state
	case controller.GameserverActionEvent:
		payloadData = ev
	case controller.BackupActionEvent:
		payloadData = ev
	case controller.WorkerActionEvent:
		payloadData = ev
	case controller.ScheduleActionEvent:
		payloadData = ev
	case controller.ScheduledTaskEvent:
		payloadData = ev
	case controller.ModActionEvent:
		payloadData = ev

	// Lifecycle events -- lightweight, just IDs
	case controller.ImagePullingEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.ContainerCreatingEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.ContainerStartedEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.GameserverReadyEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.ContainerStoppingEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.ContainerStoppedEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.ContainerExitedEvent:
		payloadData = map[string]string{"gameserver_id": ev.GameserverID}
	case controller.GameserverErrorEvent:
		payloadData = map[string]any{"gameserver_id": ev.GameserverID, "reason": ev.Reason}

	default:
		w.log.Warn("webhook: unknown event type", "type", event.EventType())
		return
	}

	eventType := event.EventType()

	for _, ep := range endpoints {
		var events []string
		if err := json.Unmarshal([]byte(ep.Events), &events); err != nil {
			w.log.Warn("webhook: invalid events filter", "endpoint_id", ep.ID, "error", err)
			continue
		}

		if !matchEventFilter(eventType, events) {
			continue
		}

		payload := WebhookPayload{
			Version:   1,
			ID:        uuid.New().String(),
			Timestamp: event.EventTimestamp(),
			EventType: eventType,
			Data:      payloadData,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			w.log.Error("webhook: failed to marshal payload", "event_type", eventType, "error", err)
			continue
		}

		now := time.Now()
		delivery := &model.WebhookDelivery{
			ID:                payload.ID,
			WebhookEndpointID: ep.ID,
			EventType:         payload.EventType,
			Payload:           string(body),
			NextAttemptAt:     now,
			CreatedAt:         now,
		}
		if err := w.store.CreateWebhookDelivery(delivery); err != nil {
			w.log.Error("webhook: failed to enqueue delivery", "endpoint_id", ep.ID, "event_type", eventType, "error", err)
		}
	}
}

// populateNode resolves node data for a gameserver using the GameserverLookup interface.
func populateNode(lookup GameserverLookup, gs *model.Gameserver) {
	if gs.NodeID == nil || *gs.NodeID == "" {
		return
	}
	node, err := lookup.GetWorkerNode(*gs.NodeID)
	if err != nil || node == nil {
		return
	}
	gs.Node = &model.GameserverNode{
		ExternalIP: node.ExternalIP,
		LanIP:      node.LanIP,
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

	ticker := time.NewTicker(deliveryPollInterval)
	defer ticker.Stop()

	lastCleanup := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
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
				w.log.Error("webhook: failed to fetch endpoint for delivery", "id", d.ID, "endpoint_id", d.WebhookEndpointID, "error", err)
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
				w.log.Info("webhook: delivered", "id", d.ID, "event_type", d.EventType, "endpoint_id", ep.ID, "response_status", statusCode)
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
			w.log.Error("webhook: delivery permanently failed", "id", d.ID, "event_type", d.EventType, "endpoint_id", ep.ID, "attempts", newAttempts, "last_error", errMsg)
			continue
		}

		backoffSec := 5 * (1 << d.Attempts) // 5, 10, 20, 40, 80, ...
		if backoffSec > maxBackoffSeconds {
			backoffSec = maxBackoffSeconds
		}
		nextAttempt := time.Now().Add(time.Duration(backoffSec) * time.Second)

		if err := w.store.MarkDeliveryRetry(d.ID, nextAttempt, errMsg); err != nil {
			w.log.Error("webhook: failed to mark delivery for retry", "id", d.ID, "error", err)
		}
		w.log.Warn("webhook: delivery failed, will retry",
			"id", d.ID, "event_type", d.EventType, "endpoint_id", ep.ID, "attempt", newAttempts,
			"next_attempt", nextAttempt.Format(time.RFC3339), "error", errMsg)
	}
}

func (w *WebhookWorker) deliver(url string, body []byte, secret string) (int, error) {
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
