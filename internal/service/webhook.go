package service

import (
	"bytes"
	"context"
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
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/google/uuid"
)

type WebhookPayload struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Data      any       `json:"data"`
}

// Payload data types — one per event category.

type statusChangedData struct {
	GameserverID string             `json:"gameserver_id"`
	OldStatus    string             `json:"old_status"`
	NewStatus    string             `json:"new_status"`
	ErrorReason  string             `json:"error_reason,omitempty"`
	Gameserver   *WebhookGameserver `json:"gameserver,omitempty"`
}

type WebhookGameserver struct {
	Name          string  `json:"name"`
	GameID        string  `json:"game_id"`
	NodeID        *string `json:"node_id"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
}

type gameserverEventData struct {
	ActorTokenID  *string `json:"actor_token_id,omitempty"`
	GameserverID  string  `json:"gameserver_id"`
	Name          string  `json:"name"`
	GameID        string  `json:"game_id"`
	NodeID        *string `json:"node_id"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
}

type backupEventData struct {
	ActorTokenID *string `json:"actor_token_id,omitempty"`
	GameserverID string  `json:"gameserver_id"`
	BackupID     string  `json:"backup_id"`
	BackupName   string  `json:"backup_name,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type workerEventData struct {
	WorkerID string `json:"worker_id"`
}

type scheduledTaskEventData struct {
	GameserverID string `json:"gameserver_id"`
	ScheduleID   string `json:"schedule_id"`
	TaskType     string `json:"task_type"`
	Error        string `json:"error,omitempty"`
}

type WebhookWorker struct {
	db          *sql.DB
	broadcaster *EventBroadcaster
	client      *http.Client
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewWebhookWorker(db *sql.DB, broadcaster *EventBroadcaster, log *slog.Logger) *WebhookWorker {
	return &WebhookWorker{
		db:          db,
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

func (w *WebhookWorker) enqueueEvent(event WebhookEvent) {
	// Skip non-transition status events (e.g., query data refresh sends running->running)
	if se, ok := event.(StatusEvent); ok && se.OldStatus == se.NewStatus {
		return
	}

	endpoints, err := models.ListEnabledWebhookEndpoints(w.db)
	if err != nil {
		w.log.Error("webhook: failed to list enabled endpoints", "error", err)
		return
	}
	if len(endpoints) == 0 {
		return
	}

	// Build payload data once (shared across all endpoints)
	var payloadData any
	switch ev := event.(type) {
	case StatusEvent:
		data := statusChangedData{
			GameserverID: ev.GameserverID,
			OldStatus:    ev.OldStatus,
			NewStatus:    ev.NewStatus,
			ErrorReason:  ev.ErrorReason,
		}
		gs, err := models.GetGameserver(w.db, ev.GameserverID)
		if err != nil {
			w.log.Warn("webhook: failed to fetch gameserver for enrichment", "gameserver_id", ev.GameserverID, "error", err)
		} else if gs != nil {
			data.Gameserver = &WebhookGameserver{
				Name:          gs.Name,
				GameID:        gs.GameID,
				NodeID:        gs.NodeID,
				MemoryLimitMB: gs.MemoryLimitMB,
			}
		}
		payloadData = data

	case GameserverEvent:
		payloadData = gameserverEventData{
			ActorTokenID:  ev.ActorTokenID,
			GameserverID:  ev.GameserverID,
			Name:          ev.Name,
			GameID:        ev.GameID,
			NodeID:        ev.NodeID,
			MemoryLimitMB: ev.MemoryLimitMB,
		}

	case BackupEvent:
		payloadData = backupEventData{
			ActorTokenID: ev.ActorTokenID,
			GameserverID: ev.GameserverID,
			BackupID:     ev.BackupID,
			BackupName:   ev.BackupName,
			Error:        ev.Error,
		}

	case WorkerEvent:
		payloadData = workerEventData{
			WorkerID: ev.WorkerID,
		}

	case ScheduledTaskEvent:
		payloadData = scheduledTaskEventData{
			GameserverID: ev.GameserverID,
			ScheduleID:   ev.ScheduleID,
			TaskType:     ev.TaskType,
			Error:        ev.Error,
		}

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
			return
		}

		now := time.Now()
		delivery := &models.WebhookDelivery{
			ID:                payload.ID,
			WebhookEndpointID: ep.ID,
			EventType:         payload.EventType,
			Payload:           string(body),
			NextAttemptAt:     now,
			CreatedAt:         now,
		}
		if err := models.CreateWebhookDelivery(w.db, delivery); err != nil {
			w.log.Error("webhook: failed to enqueue delivery", "endpoint_id", ep.ID, "event_type", eventType, "error", err)
		}
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
	maxDeliveryAttempts  = 10
	deliveryPollInterval = 5 * time.Second
	maxBackoffSeconds    = 3600
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
				if pruned, err := models.PruneWebhookDeliveries(w.db); err != nil {
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
	deliveries, err := models.GetPendingDeliveries(w.db, 10)
	if err != nil {
		w.log.Error("webhook: failed to fetch pending deliveries", "error", err)
		return
	}

	// Cache endpoint lookups within this poll cycle
	endpointCache := make(map[string]*models.WebhookEndpoint)

	for _, d := range deliveries {
		ep, cached := endpointCache[d.WebhookEndpointID]
		if !cached {
			ep, err = models.GetWebhookEndpoint(w.db, d.WebhookEndpointID)
			if err != nil {
				w.log.Error("webhook: failed to fetch endpoint for delivery", "id", d.ID, "endpoint_id", d.WebhookEndpointID, "error", err)
				continue
			}
			endpointCache[d.WebhookEndpointID] = ep
		}

		if ep == nil {
			// Endpoint was deleted but CASCADE didn't clean up (shouldn't happen)
			if err := models.MarkDeliveryFailed(w.db, d.ID, "endpoint deleted"); err != nil {
				w.log.Error("webhook: failed to mark orphan delivery failed", "id", d.ID, "error", err)
			}
			continue
		}

		statusCode, deliverErr := w.deliver(ep.URL, []byte(d.Payload), ep.Secret)

		if deliverErr == nil && statusCode >= 200 && statusCode < 300 {
			if err := models.MarkDeliverySuccess(w.db, d.ID); err != nil {
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
			if err := models.MarkDeliveryFailed(w.db, d.ID, errMsg); err != nil {
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

		if err := models.MarkDeliveryRetry(w.db, d.ID, nextAttempt, errMsg); err != nil {
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
