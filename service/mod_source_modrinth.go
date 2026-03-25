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

const modrinthBaseURL = "https://api.modrinth.com/v2"

type ModrinthSource struct {
	client *http.Client
	log    *slog.Logger
}

func NewModrinthSource(log *slog.Logger) *ModrinthSource {
	return &ModrinthSource{
		client: &http.Client{Timeout: 15 * time.Second},
		log:    log,
	}
}

// Modrinth search response
type modrinthSearchResponse struct {
	Hits      []modrinthHit `json:"hits"`
	Offset    int           `json:"offset"`
	Limit     int           `json:"limit"`
	TotalHits int           `json:"total_hits"`
}

type modrinthHit struct {
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
	DateModified string `json:"date_modified"`
	ProjectType string `json:"project_type"`
	ServerSide  string `json:"server_side"`
	ClientSide  string `json:"client_side"`
}

// Modrinth version response
type modrinthVersion struct {
	ID            string           `json:"id"`
	VersionNumber string           `json:"version_number"`
	Name          string           `json:"name"`
	GameVersions  []string         `json:"game_versions"`
	Loaders       []string         `json:"loaders"`
	Files         []modrinthFile   `json:"files"`
	DatePublished string           `json:"date_published"`
}

type modrinthFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int    `json:"size"`
}

func (s *ModrinthSource) Search(ctx context.Context, query string, gameVersion string, loader string, offset int, limit int) ([]ModSearchResult, int, error) {
	if limit <= 0 {
		limit = constants.PaginationDefaultModLimit
	}

	// Build facets: array of OR-groups, AND'd together
	// Each inner array is OR'd, outer arrays are AND'd
	// No version filter on search — users pick mods first, version adapts
	var facets []string
	facets = append(facets, `["project_type:mod","project_type:plugin"]`)
	if loader != "" {
		facets = append(facets, fmt.Sprintf(`["categories:%s"]`, loader))
	}

	facetStr := "[" + joinComma(facets) + "]"

	params := url.Values{
		"query":  {query},
		"facets": {facetStr},
		"offset": {strconv.Itoa(offset)},
		"limit":  {strconv.Itoa(limit)},
	}

	var searchResp modrinthSearchResponse
	if err := s.get(ctx, "/search?"+params.Encode(), &searchResp); err != nil {
		return nil, 0, err
	}

	results := make([]ModSearchResult, 0, len(searchResp.Hits))
	for _, h := range searchResp.Hits {
		// Skip client-only mods — they don't run on dedicated servers
		if h.ServerSide == "unsupported" {
			continue
		}
		results = append(results, ModSearchResult{
			SourceID:    h.ProjectID,
			Name:        h.Title,
			Slug:        h.Slug,
			Author:      h.Author,
			Description: h.Description,
			IconURL:     h.IconURL,
			Downloads:   h.Downloads,
			UpdatedAt:   h.DateModified,
		})
	}
	return results, searchResp.TotalHits, nil
}

func (s *ModrinthSource) GetVersions(ctx context.Context, sourceID string, gameVersion string, loader string) ([]ModVersion, error) {
	// Filter by loader only — version filtering is done client-side so users
	// can see all available versions and the system can offer to change server version
	params := url.Values{}
	if loader != "" {
		loadersJSON, _ := json.Marshal([]string{loader})
		params.Set("loaders", string(loadersJSON))
	}

	path := fmt.Sprintf("/project/%s/version", url.PathEscape(sourceID))
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var modrinthVersions []modrinthVersion
	if err := s.get(ctx, path, &modrinthVersions); err != nil {
		return nil, err
	}

	var versions []ModVersion
	for _, v := range modrinthVersions {
		// Find the primary file, or fall back to first file
		var file *modrinthFile
		for i := range v.Files {
			if v.Files[i].Primary {
				file = &v.Files[i]
				break
			}
		}
		if file == nil && len(v.Files) > 0 {
			file = &v.Files[0]
		}
		if file == nil {
			continue
		}

		gv := ""
		if len(v.GameVersions) > 0 {
			gv = v.GameVersions[0]
		}
		ldr := ""
		if len(v.Loaders) > 0 {
			ldr = v.Loaders[0]
		}

		versions = append(versions, ModVersion{
			VersionID:    v.ID,
			Version:      v.VersionNumber,
			FileName:     file.Filename,
			DownloadURL:  file.URL,
			GameVersion:  gv,
			GameVersions: v.GameVersions,
			Loader:       ldr,
		})
	}

	if versions == nil {
		versions = []ModVersion{}
	}
	return versions, nil
}

func (s *ModrinthSource) Download(ctx context.Context, versionID string) ([]byte, string, error) {
	// Fetch version details to get the download URL
	var version modrinthVersion
	path := fmt.Sprintf("/version/%s", url.PathEscape(versionID))
	if err := s.get(ctx, path, &version); err != nil {
		return nil, "", err
	}

	var file *modrinthFile
	for i := range version.Files {
		if version.Files[i].Primary {
			file = &version.Files[i]
			break
		}
	}
	if file == nil && len(version.Files) > 0 {
		file = &version.Files[0]
	}
	if file == nil {
		return nil, "", fmt.Errorf("modrinth version %s has no files", versionID)
	}

	// Download the file
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating modrinth download request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("modrinth download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("modrinth download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxModDownloadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("reading modrinth download: %w", err)
	}

	return data, file.Filename, nil
}

func (s *ModrinthSource) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modrinthBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("creating modrinth request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("modrinth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound("not found on modrinth")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("modrinth returned status %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func joinComma(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}
