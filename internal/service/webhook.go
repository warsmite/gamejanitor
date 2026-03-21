package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type WebhookPayload struct {
	ID           string    `json:"id"`
	Timestamp    time.Time `json:"timestamp"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	StatusCode   int       `json:"status_code"`
}

type WebhookSender struct {
	settingsSvc *SettingsService
	client      *http.Client
	log         *slog.Logger
}

func NewWebhookSender(settingsSvc *SettingsService, log *slog.Logger) *WebhookSender {
	return &WebhookSender{
		settingsSvc: settingsSvc,
		client:      &http.Client{Timeout: 10 * time.Second},
		log:         log,
	}
}

func (w *WebhookSender) Send(payload WebhookPayload) {
	if !w.settingsSvc.GetWebhookEnabled() {
		return
	}

	url := w.settingsSvc.GetWebhookURL()
	if url == "" {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		w.log.Error("webhook: failed to marshal payload", "action", payload.Action, "error", err)
		return
	}

	secret := w.settingsSvc.GetWebhookSecret()

	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			backoff := time.Duration(1<<(attempt-2)) * time.Second
			time.Sleep(backoff)
		}

		statusCode, err := w.deliver(url, body, secret)
		if err != nil {
			w.log.Warn("webhook: delivery failed",
				"url", url, "action", payload.Action, "attempt", attempt,
				"max_attempts", maxAttempts, "error", err)
			continue
		}

		if statusCode >= 200 && statusCode < 300 {
			w.log.Info("webhook: delivered",
				"url", url, "action", payload.Action, "resource_id", payload.ResourceID,
				"response_status", statusCode, "attempt", attempt)
			return
		}

		w.log.Warn("webhook: non-2xx response",
			"url", url, "action", payload.Action, "response_status", statusCode,
			"attempt", attempt, "max_attempts", maxAttempts)
	}

	w.log.Error("webhook: all attempts exhausted",
		"url", url, "action", payload.Action, "resource_id", payload.ResourceID)
}

func (w *WebhookSender) deliver(url string, body []byte, secret string) (int, error) {
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

// SendTest sends a test webhook payload and returns the result directly.
func (w *WebhookSender) SendTest() (statusCode int, err error) {
	url := w.settingsSvc.GetWebhookURL()
	if url == "" {
		return 0, fmt.Errorf("no webhook URL configured")
	}

	payload := WebhookPayload{
		ID:           "test",
		Timestamp:    time.Now().UTC(),
		Action:       "webhook.test",
		ResourceType: "webhook",
		ResourceID:   "",
		StatusCode:   200,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshaling test payload: %w", err)
	}

	secret := w.settingsSvc.GetWebhookSecret()
	return w.deliver(url, body, secret)
}
