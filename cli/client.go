package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var sdkClient *gamejanitor.Client

func getClient() *gamejanitor.Client {
	if sdkClient == nil {
		baseURL, token := resolveClusterContext()
		opts := []gamejanitor.Option{}
		if token != "" {
			opts = append(opts, gamejanitor.WithToken(token))
		}
		sdkClient = gamejanitor.New(baseURL, opts...)
	}
	return sdkClient
}

func ctx() context.Context {
	return context.Background()
}

// --- JSON output helper ---

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// --- Output helpers ---

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// gameserverPill derives a one-word display string from a gameserver's
// primary facts — purely a display concern, so it lives in the CLI. The
// controller exposes DesiredState + ProcessState + Ready + Operation +
// ErrorReason + WorkerOnline; we compress them into a pill here for human
// output.
func gameserverPill(gs *gamejanitor.Gameserver) string {
	if gs == nil {
		return ""
	}
	if gs.Operation != nil && gs.Operation.Phase == "deleting" {
		return "deleting"
	}
	if gs.DesiredState == "archived" {
		return "archived"
	}
	if !gs.WorkerOnline {
		return "unreachable"
	}
	if gs.Operation != nil {
		switch gs.Operation.Phase {
		case "pulling_image", "downloading_game", "installing":
			return "installing"
		case "stopping":
			return "stopping"
		case "starting":
			return "starting"
		case "migrating":
			return "migrating"
		}
	}
	if gs.ErrorReason != "" {
		return "error"
	}
	if gs.ProcessState == "running" && gs.Ready {
		return "running"
	}
	return "stopped"
}

// colorStatus applies color to gameserver status strings.
// Green=running, yellow=installing/starting/stopping, red=error/crashed, gray=stopped/unknown.
// Returns the string unchanged when NO_COLOR is set or in JSON mode.
func colorStatus(status string) string {
	if jsonOutput || os.Getenv("NO_COLOR") != "" {
		return status
	}
	switch status {
	case "running", "ready":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(status)
	case "installing", "starting", "stopping", "updating", "migrating", "restoring":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(status)
	case "error", "crashed", "install_failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(status)
	case "stopped", "created":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(status)
	default:
		return status
	}
}

func formatMemory(mb int) string {
	if mb == 0 {
		return "unlimited"
	}
	if mb >= 1024 {
		gb := float64(mb) / 1024
		if gb == float64(int(gb)) {
			return fmt.Sprintf("%d GB", int(gb))
		}
		return fmt.Sprintf("%.1f GB", gb)
	}
	return fmt.Sprintf("%d MB", mb)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func exitError(err error) error {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
	return nil
}

// --- Confirmation ---

func confirmAction(prompt string) bool {
	if skipConfirmation || jsonOutput {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes"
}

// --- ID Resolution ---

type namedEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var cachedGameservers []namedEntry
var cachedBackups = map[string][]namedEntry{}
var cachedSchedules = map[string][]namedEntry{}

func resolveID(identifier, resourceType string, entries []namedEntry) (string, error) {
	// Exact ID match
	for _, e := range entries {
		if e.ID == identifier {
			return e.ID, nil
		}
	}

	// UUID prefix match (min 2 chars)
	if len(identifier) >= 2 {
		var matches []namedEntry
		lower := strings.ToLower(identifier)
		for _, e := range entries {
			if strings.HasPrefix(strings.ToLower(e.ID), lower) {
				matches = append(matches, e)
			}
		}
		if len(matches) == 1 {
			return matches[0].ID, nil
		}
		if len(matches) > 1 {
			names := make([]string, len(matches))
			for i, e := range matches {
				names[i] = fmt.Sprintf("%s (%s)", e.Name, e.ID[:8])
			}
			return "", fmt.Errorf("ambiguous ID prefix %q matches %d %ss: %s", identifier, len(matches), resourceType, strings.Join(names, ", "))
		}
	}

	// Case-insensitive name match
	var matches []namedEntry
	for _, e := range entries {
		if strings.EqualFold(e.Name, identifier) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0].ID, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous name %q matches %d %ss", identifier, len(matches), resourceType)
	}

	return "", fmt.Errorf("no %s found matching %q", resourceType, identifier)
}

func fetchGameserverList() ([]namedEntry, error) {
	if cachedGameservers != nil {
		return cachedGameservers, nil
	}
	resp, err := getClient().Gameservers.List(ctx(), nil)
	if err != nil {
		return nil, err
	}
	cachedGameservers = make([]namedEntry, len(resp))
	for i, gs := range resp {
		cachedGameservers[i] = namedEntry{ID: gs.ID, Name: gs.Name}
	}
	return cachedGameservers, nil
}

func resolveGameserverID(identifier string) (string, error) {
	entries, err := fetchGameserverList()
	if err != nil {
		return "", err
	}
	return resolveID(identifier, "gameserver", entries)
}

// gameserverName looks up a name from the cached gameserver list. Returns short ID as fallback.
func gameserverName(id string) string {
	for _, e := range cachedGameservers {
		if e.ID == id {
			return e.Name
		}
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func resolveBackupID(gsID, identifier string) (string, error) {
	entries, ok := cachedBackups[gsID]
	if !ok {
		backups, err := getClient().Backups.List(ctx(), gsID, nil)
		if err != nil {
			return "", err
		}
		entries = make([]namedEntry, len(backups))
		for i, b := range backups {
			entries[i] = namedEntry{ID: b.ID, Name: b.Name}
		}
		cachedBackups[gsID] = entries
	}
	return resolveID(identifier, "backup", entries)
}

func resolveScheduleID(gsID, identifier string) (string, error) {
	entries, ok := cachedSchedules[gsID]
	if !ok {
		schedules, err := getClient().Schedules.List(ctx(), gsID)
		if err != nil {
			return "", err
		}
		entries = make([]namedEntry, len(schedules))
		for i, s := range schedules {
			entries[i] = namedEntry{ID: s.ID, Name: s.Name}
		}
		cachedSchedules[gsID] = entries
	}
	return resolveID(identifier, "schedule", entries)
}
