package cli

import (
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
