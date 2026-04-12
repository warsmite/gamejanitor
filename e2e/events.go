//go:build e2e || smoke

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// sseEvent represents a parsed SSE event from the /api/events stream.
type sseEvent struct {
	Type         string `json:"type"`
	GameserverID string `json:"gameserver_id"`
	Status       string `json:"status,omitempty"`
	ErrorReason  string `json:"error_reason,omitempty"`
}

// eventStream subscribes to the SSE event stream and broadcasts to registered listeners.
// No buffering — listeners only receive events that arrive while they're registered.
type eventStream struct {
	mu        sync.Mutex
	listeners []listener
	cancel    context.CancelFunc
}

type listener struct {
	gsID  string
	match func(sseEvent) bool
	ch    chan sseEvent
}

func newEventStream(baseURL string) *eventStream {
	ctx, cancel := context.WithCancel(context.Background())
	es := &eventStream{cancel: cancel}
	go es.consume(ctx, baseURL+"/api/events")
	return es
}

func (es *eventStream) consume(ctx context.Context, url string) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		var ev sseEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}

		es.mu.Lock()
		for i := range es.listeners {
			l := &es.listeners[i]
			if l.gsID == ev.GameserverID && l.match(ev) {
				select {
				case l.ch <- ev:
				default:
				}
			}
		}
		es.mu.Unlock()
	}
}

// Listen registers a listener that receives matching events for a gameserver.
// Returns a channel and an unregister function. The channel is buffered(8) so
// multiple events can queue before the caller reads them.
func (es *eventStream) Listen(gsID string, match func(sseEvent) bool) (<-chan sseEvent, func()) {
	ch := make(chan sseEvent, 8)
	l := listener{gsID: gsID, match: match, ch: ch}

	es.mu.Lock()
	es.listeners = append(es.listeners, l)
	es.mu.Unlock()

	unlisten := func() {
		es.mu.Lock()
		defer es.mu.Unlock()
		for i, ll := range es.listeners {
			if ll.ch == ch {
				es.listeners = append(es.listeners[:i], es.listeners[i+1:]...)
				break
			}
		}
	}
	return ch, unlisten
}

func (es *eventStream) Stop() {
	if es.cancel != nil {
		es.cancel()
	}
}
