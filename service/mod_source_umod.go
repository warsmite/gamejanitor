package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/warsmite/gamejanitor/constants"
)

type UmodSource struct {
	client *http.Client
	log    *slog.Logger
}

func NewUmodSource(log *slog.Logger) *UmodSource {
	return &UmodSource{
		client: &http.Client{Timeout: 15 * time.Second},
		log:    log,
	}
}

// uMod search API response
type umodSearchResponse struct {
	Data       []umodPlugin `json:"data"`
	TotalPages int          `json:"total_pages"`
	TotalCount int          `json:"total_count"`
}

type umodPlugin struct {
	Name              string `json:"name"`
	Title             string `json:"title"`
	Description       string `json:"description_short"`
	Author            string `json:"author"`
	IconURL           string `json:"icon_url"`
	LatestReleaseAt   string `json:"latest_release_at"`
	DownloadsTotal    int    `json:"downloads_total"`
	LatestReleaseVer  string `json:"latest_release_version"`
	JSONUrl           string `json:"json_url"`
}

// uMod plugin detail response (for download URL)
type umodPluginDetail struct {
	Name             string `json:"name"`
	Title            string `json:"title"`
	Description      string `json:"description_short"`
	Author           string `json:"author"`
	IconURL          string `json:"icon_url"`
	DownloadsTotal   int    `json:"downloads_total"`
	LatestReleaseAt  string `json:"latest_release_at"`
	LatestReleaseVer string `json:"latest_release_version"`
	DownloadURL      string `json:"download_url"`
	Filename         string `json:"filename"`
}

func (s *UmodSource) Search(ctx context.Context, query string, gameVersion string, loader string, offset int, limit int) ([]ModSearchResult, int, error) {
	if limit <= 0 {
		limit = constants.PaginationDefaultModLimit
	}
	page := (offset / limit) + 1

	params := url.Values{
		"query":   {query},
		"page":    {strconv.Itoa(page)},
		"sort":    {"title"},
		"sortdir": {"asc"},
	}
	// Filter to Rust plugins (uMod hosts plugins for multiple games)
	params.Set("categories", "rust")

	reqURL := "https://umod.org/plugins/search.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating umod search request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("umod search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("umod search returned status %d", resp.StatusCode)
	}

	var searchResp umodSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, 0, fmt.Errorf("decoding umod search response: %w", err)
	}

	results := make([]ModSearchResult, 0, len(searchResp.Data))
	for _, p := range searchResp.Data {
		results = append(results, ModSearchResult{
			SourceID:    p.Name,
			Name:        p.Title,
			Slug:        p.Name,
			Author:      p.Author,
			Description: p.Description,
			IconURL:     p.IconURL,
			Downloads:   p.DownloadsTotal,
			UpdatedAt:   p.LatestReleaseAt,
		})
	}

	return results, searchResp.TotalCount, nil
}

func (s *UmodSource) GetVersions(ctx context.Context, sourceID string, gameVersion string, loader string) ([]ModVersion, error) {
	detail, err := s.fetchPluginDetail(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	// uMod plugins have a single current version
	versions := []ModVersion{
		{
			VersionID:   detail.LatestReleaseVer,
			Version:     detail.LatestReleaseVer,
			FileName:    detail.Filename,
			DownloadURL: detail.DownloadURL,
		},
	}
	return versions, nil
}

func (s *UmodSource) Download(ctx context.Context, versionID string) ([]byte, string, error) {
	// versionID for uMod is the version string; we need the plugin name to look up the download URL.
	// The ModService passes sourceID as versionID for uMod since there's only one version.
	// We handle this by having ModService call GetVersions first, then use the DownloadURL directly.
	// This method is not called directly for uMod — see ModService.Install.
	return nil, "", fmt.Errorf("umod Download should not be called directly; use DownloadFromURL")
}

// DownloadFromURL fetches a mod file from a direct URL.
func (s *UmodSource) DownloadFromURL(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUmodDownloadBytes))
	if err != nil {
		return nil, fmt.Errorf("reading download body: %w", err)
	}
	return data, nil
}

func (s *UmodSource) fetchPluginDetail(ctx context.Context, pluginName string) (*umodPluginDetail, error) {
	reqURL := fmt.Sprintf("https://umod.org/plugins/%s.json", url.PathEscape(pluginName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating umod detail request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("umod detail request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFoundf("umod plugin %q not found", pluginName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("umod detail returned status %d", resp.StatusCode)
	}

	var detail umodPluginDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decoding umod detail response: %w", err)
	}
	return &detail, nil
}
