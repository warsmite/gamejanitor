package mod

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
)

type WorkshopCatalog struct {
	client      *http.Client
	log         *slog.Logger
	settingsSvc *settings.SettingsService
}

func NewWorkshopCatalog(settingsSvc *settings.SettingsService, log *slog.Logger) *WorkshopCatalog {
	return &WorkshopCatalog{
		client:      &http.Client{Timeout: 15 * time.Second},
		log:         log,
		settingsSvc: settingsSvc,
	}
}

func (c *WorkshopCatalog) Search(ctx context.Context, query string, filters CatalogFilters, offset, limit int) ([]ModResult, int, error) {
	key := c.settingsSvc.GetString(settings.SettingSteamAPIKey)
	if key == "" {
		return nil, 0, controller.ErrBadRequest("Steam Workshop search requires a Steam API key. Configure it in Settings, or paste a Workshop item ID directly.")
	}

	params := url.Values{
		"key":                      {key},
		"search_text":              {query},
		"return_short_description": {"true"},
		"return_previews":          {"true"},
		"numperpage":               {fmt.Sprintf("%d", limit)},
		"cursor":                   {"*"},
		"query_type":               {"1"},
	}
	if appID := filters.Extra["app_id"]; appID != "" {
		params.Set("appid", appID)
	}

	reqURL := "https://api.steampowered.com/IPublishedFileService/QueryFiles/v1/?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating workshop search request: %w", err)
	}

	resp, err := c.client.Do(req)
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

	results := make([]ModResult, 0, len(steamResp.Response.PublishedFiles))
	for _, f := range steamResp.Response.PublishedFiles {
		results = append(results, ModResult{
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

func (c *WorkshopCatalog) GetDetails(ctx context.Context, modID string) (*ModDetails, error) {
	detail, err := c.getItemDetails(ctx, modID)
	if err != nil {
		return nil, err
	}
	return &ModDetails{
		SourceID:    modID,
		Name:        detail.Title,
		Description: detail.ShortDesc,
	}, nil
}

func (c *WorkshopCatalog) GetVersions(ctx context.Context, modID string, filters CatalogFilters) ([]ModVersion, error) {
	detail, err := c.getItemDetails(ctx, modID)
	if err != nil {
		return nil, err
	}

	// Workshop items don't have discrete versions
	return []ModVersion{
		{
			VersionID: modID,
			Version:   "",
			FileName:  detail.Title,
		},
	}, nil
}

// GetDependencies returns nil — Steam handles deps at download time.
func (c *WorkshopCatalog) GetDependencies(ctx context.Context, versionID string) ([]ModDependency, error) {
	return nil, nil
}

type workshopItemDetail struct {
	Title     string
	ShortDesc string
}

func (c *WorkshopCatalog) getItemDetails(ctx context.Context, fileID string) (*workshopItemDetail, error) {
	form := url.Values{
		"itemcount":           {"1"},
		"publishedfileids[0]": {fileID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating workshop detail request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workshop detail request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workshop detail returned status %d", resp.StatusCode)
	}

	var steamResp struct {
		Response struct {
			PublishedFileDetails []struct {
				Result int    `json:"result"`
				Title  string `json:"title"`
				Desc   string `json:"description"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steamResp); err != nil {
		return nil, fmt.Errorf("decoding workshop detail response: %w", err)
	}

	if len(steamResp.Response.PublishedFileDetails) == 0 || steamResp.Response.PublishedFileDetails[0].Result != 1 {
		return nil, controller.ErrNotFoundf("workshop item %s not found", fileID)
	}

	d := steamResp.Response.PublishedFileDetails[0]
	desc := d.Desc
	if len(desc) > 200 {
		desc = desc[:200] + "..."
	}

	return &workshopItemDetail{
		Title:     d.Title,
		ShortDesc: desc,
	}, nil
}
