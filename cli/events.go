package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Query or stream events",
	RunE:  runEvents,
}

func init() {
	eventsCmd.Flags().String("type", "", "Filter by event type (glob pattern, e.g. gameserver.*)")
	eventsCmd.Flags().String("gameserver", "", "Filter by gameserver (name or ID)")
	eventsCmd.Flags().Int("limit", 50, "Number of events to show")
	eventsCmd.Flags().BoolP("follow", "f", false, "Stream live events via SSE")
}

func runEvents(cmd *cobra.Command, args []string) error {
	follow, _ := cmd.Flags().GetBool("follow")

	if follow {
		return runEventsFollow(cmd)
	}

	return runEventsHistory(cmd)
}

func runEventsHistory(cmd *cobra.Command) error {
	params := url.Values{}

	if v, _ := cmd.Flags().GetString("type"); v != "" {
		params.Set("type", v)
	}
	if v, _ := cmd.Flags().GetString("gameserver"); v != "" {
		gsID, err := resolveGameserverID(v)
		if err != nil {
			return exitError(err)
		}
		params.Set("gameserver_id", gsID)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}

	path := "/api/events/history"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := apiGet(path)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSONResponse(resp)
		return nil
	}

	var events []struct {
		ID           string `json:"id"`
		EventType    string `json:"event_type"`
		GameserverID string `json:"gameserver_id"`
		Summary      string `json:"summary"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(resp.Data, &events); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if len(events) == 0 {
		fmt.Println("No events found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "TIME\tTYPE\tSUMMARY")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.CreatedAt, e.EventType, e.Summary)
	}
	w.Flush()
	return nil
}

func runEventsFollow(cmd *cobra.Command) error {
	resolvedURL, resolvedToken := resolveClusterContext()

	params := url.Values{}
	if v, _ := cmd.Flags().GetString("type"); v != "" {
		params.Set("types", v)
	}

	sseURL := strings.TrimRight(resolvedURL, "/") + "/api/events"
	if len(params) > 0 {
		sseURL += "?" + params.Encode()
	}

	req, err := http.NewRequest("GET", sseURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if resolvedToken != "" {
		req.Header.Set("Authorization", "Bearer "+resolvedToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return exitError(fmt.Errorf("cannot connect to gamejanitor at %s", resolvedURL))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return exitError(fmt.Errorf("SSE connection failed: HTTP %d", resp.StatusCode))
	}

	if !jsonOutput {
		fmt.Println("Streaming events (Ctrl+C to stop)...")
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "event: <type>\ndata: <json>\n\n"
		// Skip comments (heartbeats) and empty lines
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := line[6:]

			if jsonOutput {
				fmt.Println(data)
				continue
			}

			var event struct {
				EventType    string `json:"event_type"`
				GameserverID string `json:"gameserver_id"`
				Summary      string `json:"summary"`
				CreatedAt    string `json:"created_at"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				fmt.Println(data)
				continue
			}

			if event.Summary != "" {
				fmt.Printf("[%s] %s: %s\n", event.CreatedAt, event.EventType, event.Summary)
			} else {
				fmt.Printf("[%s] %s\n", event.CreatedAt, event.EventType)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE stream error: %w", err)
	}

	return nil
}
