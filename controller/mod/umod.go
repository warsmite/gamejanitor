package mod

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

	"github.com/warsmite/gamejanitor/controller"
)

type UmodCatalog struct {
	client   *http.Client
	log      *slog.Logger
	category string // game-specific: "rust", "hurtworld", etc.
}

func NewUmodCatalog(category string, log *slog.Logger) *UmodCatalog {
	return &UmodCatalog{
		client:   &http.Client{Timeout: 15 * time.Second},
		log:      log,
		category: category,
	}
}

// uMod API response types

type umodSearchResponse struct {
	Data       []umodPlugin `json:"data"`
	TotalPages int          `json:"total_pages"`
	TotalCount int          `json:"total_count"`
}

type umodPlugin struct {
	Name             string `json:"name"`
	Title            string `json:"title"`
	Description      string `json:"description_short"`
	Author           string `json:"author"`
	IconURL          string `json:"icon_url"`
	LatestReleaseAt  string `json:"latest_release_at"`
	DownloadsTotal   int    `json:"downloads_total"`
	LatestReleaseVer string `json:"latest_release_version"`
}

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

func (c *UmodCatalog) Search(ctx context.Context, query string, filters CatalogFilters) ([]ModResult, int, error) {
	limit := 20
	page := 1

	params := url.Values{
		"query":   {query},
		"page":    {strconv.Itoa(page)},
		"sort":    {"title"},
		"sortdir": {"asc"},
	}
	if c.category != "" {
		params.Set("categories", c.category)
	}

	reqURL := "https://umod.org/plugins/search.json?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating umod search request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := c.client.Do(req)
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

	results := make([]ModResult, 0, len(searchResp.Data))
	for _, p := range searchResp.Data {
		results = append(results, ModResult{
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

	_ = limit // used for pagination in the future
	return results, searchResp.TotalCount, nil
}

func (c *UmodCatalog) GetDetails(ctx context.Context, modID string) (*ModDetails, error) {
	detail, err := c.fetchPluginDetail(ctx, modID)
	if err != nil {
		return nil, err
	}
	return &ModDetails{
		SourceID:    detail.Name,
		Name:        detail.Title,
		Description: detail.Description,
		Author:      detail.Author,
		IconURL:     detail.IconURL,
		Downloads:   detail.DownloadsTotal,
	}, nil
}

func (c *UmodCatalog) GetVersions(ctx context.Context, modID string, filters CatalogFilters) ([]ModVersion, error) {
	detail, err := c.fetchPluginDetail(ctx, modID)
	if err != nil {
		return nil, err
	}

	// uMod plugins have a single current version
	return []ModVersion{
		{
			VersionID:   detail.LatestReleaseVer,
			Version:     detail.LatestReleaseVer,
			FileName:    detail.Filename,
			DownloadURL: detail.DownloadURL,
		},
	}, nil
}

// GetDependencies returns nil — uMod plugins are standalone.
func (c *UmodCatalog) GetDependencies(ctx context.Context, versionID string) ([]ModDependency, error) {
	return nil, nil
}

func (c *UmodCatalog) fetchPluginDetail(ctx context.Context, pluginName string) (*umodPluginDetail, error) {
	reqURL := fmt.Sprintf("https://umod.org/plugins/%s.json", url.PathEscape(pluginName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating umod detail request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("umod detail request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, controller.ErrNotFoundf("umod plugin %q not found", pluginName)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("umod detail returned status %d: %s", resp.StatusCode, string(body))
	}

	var detail umodPluginDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decoding umod detail response: %w", err)
	}
	return &detail, nil
}
