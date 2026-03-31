package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
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
	opts := &gamejanitor.EventHistoryOptions{}

	if v, _ := cmd.Flags().GetString("type"); v != "" {
		opts.Type = v
	}
	if v, _ := cmd.Flags().GetString("gameserver"); v != "" {
		gsID, err := resolveGameserverID(v)
		if err != nil {
			return exitError(err)
		}
		opts.GameserverID = gsID
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		opts.Limit = v
	}

	events, err := getClient().Events.History(ctx(), opts)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(events)
		return nil
	}

	if len(events) == 0 {
		fmt.Println("No events found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "TIME\tTYPE")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\n", e.CreatedAt.Format("2006-01-02T15:04:05Z"), e.Type)
	}
	w.Flush()
	return nil
}

func runEventsFollow(cmd *cobra.Command) error {
	var typeFilters []string
	if v, _ := cmd.Flags().GetString("type"); v != "" {
		typeFilters = append(typeFilters, v)
	}

	ch, err := getClient().Events.Subscribe(ctx(), typeFilters...)
	if err != nil {
		return exitError(err)
	}

	if !jsonOutput {
		fmt.Println("Streaming events (Ctrl+C to stop)...")
	}

	for event := range ch {
		if jsonOutput {
			fmt.Println(string(event.Data))
			continue
		}

		var parsed struct {
			EventType    string `json:"event_type"`
			GameserverID string `json:"gameserver_id"`
			Summary      string `json:"summary"`
			CreatedAt    string `json:"created_at"`
		}
		if err := json.Unmarshal(event.Data, &parsed); err != nil {
			fmt.Println(string(event.Data))
			continue
		}

		if parsed.Summary != "" {
			fmt.Printf("[%s] %s: %s\n", parsed.CreatedAt, parsed.EventType, parsed.Summary)
		} else {
			fmt.Printf("[%s] %s\n", parsed.CreatedAt, parsed.EventType)
		}
	}

	return nil
}
