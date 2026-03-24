package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WorkshopSource struct {
	settingsSvc *SettingsService
	client      *http.Client
	log         *slog.Logger
}

func NewWorkshopSource(settingsSvc *SettingsService, log *slog.Logger) *WorkshopSource {
	return &WorkshopSource{
		settingsSvc: settingsSvc,
		client:      &http.Client{Timeout: 15 * time.Second},
		log:         log,
	}
}

func (s *WorkshopSource) apiKey() string {
	return s.settingsSvc.GetString("steam_api_key")
}

func (s *WorkshopSource) Search(ctx context.Context, query string, gameVersion string, loader string, offset int, limit int) ([]ModSearchResult, int, error) {
	key := s.apiKey()
	if key == "" {
		return nil, 0, ErrBadRequest("Steam Workshop search requires a Steam API key. Configure it in Settings, or paste a Workshop item ID directly.")
	}

	// loader is unused for workshop; gameVersion maps to app_id but we get it from the caller's context
	// The appID is passed via the query param convention: "appid:{id} {query}"
	// Actually, ModService resolves the appID from the game config and passes it differently.
	// For now, we parse appID from the loader field (ModService sets this).
	appID := loader // ModService passes appID as the "loader" for workshop

	params := url.Values{
		"key":                 {key},
		"search_text":         {query},
		"return_short_description": {"true"},
		"return_previews":     {"true"},
		"numperpage":          {fmt.Sprintf("%d", limit)},
		"cursor":              {"*"}, // First page
		"query_type":          {"1"}, // RankedByTrend
	}
	if appID != "" {
		params.Set("appid", appID)
	}

	reqURL := "https://api.steampowered.com/IPublishedFileService/QueryFiles/v1/?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating workshop search request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("workshop search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("workshop search returned status %d", resp.StatusCode)
	}

	var steamResp struct {
		Response struct {
			Total          int `json:"total"`
			PublishedFiles []struct {
				PublishedFileID string `json:"publishedfileid"`
				Title          string `json:"title"`
				ShortDesc      string `json:"short_description"`
				PreviewURL     string `json:"preview_url"`
				Creator        string `json:"creator"`
				Subscriptions  int    `json:"lifetime_subscriptions"`
				TimeUpdated    int64  `json:"time_updated"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steamResp); err != nil {
		return nil, 0, fmt.Errorf("decoding workshop search response: %w", err)
	}

	results := make([]ModSearchResult, 0, len(steamResp.Response.PublishedFiles))
	for _, f := range steamResp.Response.PublishedFiles {
		results = append(results, ModSearchResult{
			SourceID:    f.PublishedFileID,
			Name:        f.Title,
			Slug:        f.PublishedFileID,
			Author:      f.Creator,
			Description: f.ShortDesc,
			IconURL:     f.PreviewURL,
			Downloads:   f.Subscriptions,
			UpdatedAt:   time.Unix(f.TimeUpdated, 0).Format(time.RFC3339),
		})
	}

	return results, steamResp.Response.Total, nil
}

func (s *WorkshopSource) GetVersions(ctx context.Context, sourceID string, gameVersion string, loader string) ([]ModVersion, error) {
	// Fetch item details for metadata
	detail, err := s.getItemDetails(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	// Workshop items don't have discrete versions — return a single entry with the title as FileName
	return []ModVersion{
		{
			VersionID: sourceID,
			Version:   "",
			FileName:  detail.Title,
		},
	}, nil
}

func (s *WorkshopSource) Download(ctx context.Context, versionID string) ([]byte, string, error) {
	// Workshop downloads happen via steamcmd inside the container, not via the API
	return nil, "", fmt.Errorf("workshop items are downloaded by steamcmd at server start, not via direct download")
}

type workshopItemDetail struct {
	Title      string
	PreviewURL string
	ShortDesc  string
}

func (s *WorkshopSource) getItemDetails(ctx context.Context, fileID string) (*workshopItemDetail, error) {
	// Use GetPublishedFileDetails which doesn't require an API key
	form := url.Values{
		"itemcount":             {"1"},
		"publishedfileids[0]":   {fileID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating workshop detail request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workshop detail request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workshop detail returned status %d", resp.StatusCode)
	}

	var steamResp struct {
		Response struct {
			Result             int `json:"result"`
			ResultCount        int `json:"resultcount"`
			PublishedFileDetails []struct {
				Result         int    `json:"result"`
				PublishedFileID string `json:"publishedfileid"`
				Title          string `json:"title"`
				Description    string `json:"description"`
				PreviewURL     string `json:"preview_url"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steamResp); err != nil {
		return nil, fmt.Errorf("decoding workshop detail response: %w", err)
	}

	if len(steamResp.Response.PublishedFileDetails) == 0 || steamResp.Response.PublishedFileDetails[0].Result != 1 {
		return nil, ErrNotFoundf("workshop item %s not found", fileID)
	}

	d := steamResp.Response.PublishedFileDetails[0]
	desc := d.Description
	if len(desc) > 200 {
		desc = desc[:200] + "..."
	}

	return &workshopItemDetail{
		Title:      d.Title,
		PreviewURL: d.PreviewURL,
		ShortDesc:  desc,
	}, nil
}
