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

func apiPut(path string, body any) (*apiResponse, error) {
	return apiRequest("PUT", path, body)
}

func apiDelete(path string) (*apiResponse, error) {
	return apiRequest("DELETE", path, nil)
}

func apiRequest(method, path string, body any) (*apiResponse, error) {
	url := strings.TrimRight(apiURL, "/") + path

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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to gamejanitor at %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	// 204 No Content has no body
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

// printJSONResponse prints the raw API response as indented JSON.
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

// printJSONData prints just the data field as indented JSON.
func printJSONData(data json.RawMessage) {
	var v any
	json.Unmarshal(data, &v)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func exitError(err error) error {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
	return nil
}

// Gameserver resolution: resolve a name or UUID prefix to a full gameserver ID.

type gameserverEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var cachedGameservers []gameserverEntry

func fetchGameserverList() ([]gameserverEntry, error) {
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
	gameservers, err := fetchGameserverList()
	if err != nil {
		return "", err
	}

	// Exact ID match
	for _, gs := range gameservers {
		if gs.ID == identifier {
			return gs.ID, nil
		}
	}

	// UUID prefix match (min 4 chars)
	if len(identifier) >= 4 {
		var prefixMatches []gameserverEntry
		lower := strings.ToLower(identifier)
		for _, gs := range gameservers {
			if strings.HasPrefix(strings.ToLower(gs.ID), lower) {
				prefixMatches = append(prefixMatches, gs)
			}
		}
		if len(prefixMatches) == 1 {
			return prefixMatches[0].ID, nil
		}
		if len(prefixMatches) > 1 {
			names := make([]string, len(prefixMatches))
			for i, gs := range prefixMatches {
				names[i] = fmt.Sprintf("%s (%s)", gs.Name, gs.ID[:8])
			}
			return "", fmt.Errorf("ambiguous ID prefix %q matches %d gameservers: %s", identifier, len(prefixMatches), strings.Join(names, ", "))
		}
	}

	// Case-insensitive name match
	var nameMatches []gameserverEntry
	for _, gs := range gameservers {
		if strings.EqualFold(gs.Name, identifier) {
			nameMatches = append(nameMatches, gs)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0].ID, nil
	}
	if len(nameMatches) > 1 {
		return "", fmt.Errorf("ambiguous name %q matches %d gameservers", identifier, len(nameMatches))
	}

	return "", fmt.Errorf("no gameserver found matching %q", identifier)
}

// Confirmation prompt for destructive actions.

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
