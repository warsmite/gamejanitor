package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
)

type apiResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func apiGet(path string) (*apiResponse, error) {
	return apiRequest("GET", path, nil)
}

func apiPost(path string, body any) (*apiResponse, error) {
	return apiRequest("POST", path, body)
}

func apiPatch(path string, body any) (*apiResponse, error) {
	return apiRequest("PATCH", path, body)
}

func apiDelete(path string) (*apiResponse, error) {
	return apiRequest("DELETE", path, nil)
}

func apiRequest(method, path string, body any) (*apiResponse, error) {
	resolvedURL, resolvedToken := resolveClusterContext()
	url := strings.TrimRight(resolvedURL, "/") + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if resolvedToken != "" {
		req.Header.Set("Authorization", "Bearer "+resolvedToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to gamejanitor at %s\n  Is the server running? Start it with: gamejanitor serve\n  Or set up a remote cluster: gamejanitor cluster add <name> --address <url> --token <token>", resolvedURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return &apiResponse{Status: "ok"}, nil
	}

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if result.Status == "error" {
		return nil, fmt.Errorf("%s", result.Error)
	}

	return &result, nil
}

// apiDownload performs a raw HTTP GET and returns the response body. Caller must close.
func apiDownload(path string) (*http.Response, error) {
	resolvedURL, resolvedToken := resolveClusterContext()
	url := strings.TrimRight(resolvedURL, "/") + path
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if resolvedToken != "" {
		req.Header.Set("Authorization", "Bearer "+resolvedToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to gamejanitor at %s", resolvedURL)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// --- JSON helpers ---

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// --- Output helpers ---

func printJSONResponse(resp *apiResponse) {
	out := map[string]any{"status": resp.Status}
	if resp.Data != nil {
		var data any
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse response data: %v\n", err)
		} else {
			out["data"] = data
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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
	resp, err := apiGet("/api/gameservers")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(resp.Data, &cachedGameservers); err != nil {
		return nil, fmt.Errorf("parsing gameserver list: %w", err)
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
		resp, err := apiGet("/api/gameservers/" + gsID + "/backups")
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(resp.Data, &entries); err != nil {
			return "", fmt.Errorf("parsing backup list: %w", err)
		}
		cachedBackups[gsID] = entries
	}
	return resolveID(identifier, "backup", entries)
}

func resolveScheduleID(gsID, identifier string) (string, error) {
	entries, ok := cachedSchedules[gsID]
	if !ok {
		resp, err := apiGet("/api/gameservers/" + gsID + "/schedules")
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(resp.Data, &entries); err != nil {
			return "", fmt.Errorf("parsing schedule list: %w", err)
		}
		cachedSchedules[gsID] = entries
	}
	return resolveID(identifier, "schedule", entries)
}
