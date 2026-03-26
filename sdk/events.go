package gamejanitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// EventService handles event-related API calls (SSE streaming and history).
type EventService struct {
	client *Client
}

// SSEEvent is a single event received from the SSE stream.
type SSEEvent struct {
	Type string
	Data json.RawMessage
}

// Subscribe opens an SSE connection and returns a channel of events.
// The channel is closed when the context is canceled or the connection drops.
// Pass type filter patterns (e.g. "gameserver.*") or nil for all events.
func (s *EventService) Subscribe(ctx context.Context, typeFilters ...string) (<-chan SSEEvent, error) {
	v := url.Values{}
	if len(typeFilters) > 0 {
		v.Set("types", strings.Join(typeFilters, ","))
	}
	path := "/api/events"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}

	req, err := s.client.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamejanitor: SSE connection failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &Error{StatusCode: resp.StatusCode, Message: fmt.Sprintf("SSE: HTTP %d", resp.StatusCode)}
	}

	ch := make(chan SSEEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		var eventType string
		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if eventType != "" {
					select {
					case ch <- SSEEvent{Type: eventType, Data: json.RawMessage(data)}:
					case <-ctx.Done():
						return
					}
					eventType = ""
				}
				continue
			}
		}
	}()

	return ch, nil
}

// EventHistoryOptions configures filters for event history.
type EventHistoryOptions struct {
	Type         string // glob pattern
	GameserverID string
	Limit        int
	Offset       int
}

// History returns historical events matching the given filters.
func (s *EventService) History(ctx context.Context, opts *EventHistoryOptions) ([]Event, error) {
	v := url.Values{}
	if opts != nil {
		if opts.Type != "" {
			v.Set("type", opts.Type)
		}
		if opts.GameserverID != "" {
			v.Set("gameserver_id", opts.GameserverID)
		}
		if opts.Limit > 0 {
			v.Set("limit", fmt.Sprintf("%d", opts.Limit))
		}
		if opts.Offset > 0 {
			v.Set("offset", fmt.Sprintf("%d", opts.Offset))
		}
	}
	path := "/api/events/history"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}

	var events []Event
	if err := s.client.get(ctx, path, &events); err != nil {
		return nil, err
	}
	return events, nil
}
