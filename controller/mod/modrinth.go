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
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller"
)

const modrinthBaseURL = "https://api.modrinth.com/v2"

type ModrinthCatalog struct {
	client *http.Client
	log    *slog.Logger
}

func NewModrinthCatalog(log *slog.Logger) *ModrinthCatalog {
	return &ModrinthCatalog{
		client: &http.Client{Timeout: 15 * time.Second},
		log:    log,
	}
}

// Modrinth API response types

type modrinthSearchResponse struct {
	Hits      []modrinthHit `json:"hits"`
	Offset    int           `json:"offset"`
	Limit     int           `json:"limit"`
	TotalHits int           `json:"total_hits"`
}

type modrinthHit struct {
	ProjectID    string `json:"project_id"`
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Author       string `json:"author"`
	IconURL      string `json:"icon_url"`
	Downloads    int    `json:"downloads"`
	DateModified string `json:"date_modified"`
	ProjectType  string `json:"project_type"`
	ServerSide   string `json:"server_side"`
}

type modrinthVersion struct {
	ID            string             `json:"id"`
	VersionNumber string             `json:"version_number"`
	Name          string             `json:"name"`
	GameVersions  []string           `json:"game_versions"`
	Loaders       []string           `json:"loaders"`
	Files         []modrinthFile     `json:"files"`
	Dependencies  []modrinthDep      `json:"dependencies"`
	DatePublished string             `json:"date_published"`
}

type modrinthFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int    `json:"size"`
}

type modrinthDep struct {
	ProjectID      string `json:"project_id"`
	VersionID      string `json:"version_id"`
	DependencyType string `json:"dependency_type"` // "required", "optional", "incompatible", "embedded"
}

type modrinthProject struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
}

func (c *ModrinthCatalog) Search(ctx context.Context, query string, filters CatalogFilters, offset, limit int) ([]ModResult, int, error) {
	if limit <= 0 {
		limit = DefaultModLimit
	}

	var facets []string
	if pt := filters.Extra["project_type"]; pt != "" {
		facets = append(facets, fmt.Sprintf(`["project_type:%s"]`, pt))
	} else {
		facets = append(facets, `["project_type:mod","project_type:plugin"]`)
	}
	if filters.Loader != "" {
		facets = append(facets, fmt.Sprintf(`["categories:%s"]`, filters.Loader))
	}
	// Exclude client-only projects — they don't work on dedicated servers
	facets = append(facets, `["server_side:required","server_side:optional"]`)

	facetStr := "[" + strings.Join(facets, ",") + "]"

	params := url.Values{
		"query":  {query},
		"facets": {facetStr},
		"offset": {strconv.Itoa(offset)},
		"limit":  {strconv.Itoa(limit)},
	}

	var searchResp modrinthSearchResponse
	if err := c.get(ctx, "/search?"+params.Encode(), &searchResp); err != nil {
		return nil, 0, err
	}

	results := make([]ModResult, 0, len(searchResp.Hits))
	for _, h := range searchResp.Hits {
		if h.ServerSide == "unsupported" {
			continue
		}
		results = append(results, ModResult{
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

func (c *ModrinthCatalog) GetDetails(ctx context.Context, modID string) (*ModDetails, error) {
	var project modrinthProject
	if err := c.get(ctx, "/project/"+url.PathEscape(modID), &project); err != nil {
		return nil, err
	}
	return &ModDetails{
		SourceID:    project.ID,
		Name:        project.Title,
		Description: project.Description,
		IconURL:     project.IconURL,
		Downloads:   project.Downloads,
	}, nil
}

func (c *ModrinthCatalog) GetVersions(ctx context.Context, modID string, filters CatalogFilters) ([]ModVersion, error) {
	params := url.Values{}
	if filters.Loader != "" {
		loadersJSON, _ := json.Marshal([]string{filters.Loader})
		params.Set("loaders", string(loadersJSON))
	}

	path := fmt.Sprintf("/project/%s/version", url.PathEscape(modID))
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var modrinthVersions []modrinthVersion
	if err := c.get(ctx, path, &modrinthVersions); err != nil {
		return nil, err
	}

	var versions []ModVersion
	for _, v := range modrinthVersions {
		var file *modrinthFile
		if filters.ServerPack {
			file = serverPackFile(v.Files)
		} else {
			file = primaryFile(v.Files)
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

		hasServer := serverPackFile(v.Files) != primaryFile(v.Files)

		versions = append(versions, ModVersion{
			VersionID:     v.ID,
			Version:       v.VersionNumber,
			FileName:      file.Filename,
			DownloadURL:   file.URL,
			GameVersion:   gv,
			GameVersions:  v.GameVersions,
			HasServerFile: hasServer,
			Loader:       ldr,
		})
	}

	if versions == nil {
		versions = []ModVersion{}
	}
	return versions, nil
}

func (c *ModrinthCatalog) GetDependencies(ctx context.Context, versionID string) ([]ModDependency, error) {
	var version modrinthVersion
	path := fmt.Sprintf("/version/%s", url.PathEscape(versionID))
	if err := c.get(ctx, path, &version); err != nil {
		return nil, err
	}

	var deps []ModDependency
	for _, d := range version.Dependencies {
		if d.ProjectID == "" {
			continue
		}
		dep := ModDependency{
			ModID:     d.ProjectID,
			VersionID: d.VersionID,
		}
		switch d.DependencyType {
		case "required":
			dep.Required = true
		case "optional":
			dep.Required = false
		default:
			continue // skip incompatible, embedded
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func (c *ModrinthCatalog) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modrinthBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("creating modrinth request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("modrinth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return controller.ErrNotFound("not found on modrinth")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("modrinth returned status %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func primaryFile(files []modrinthFile) *modrinthFile {
	for i := range files {
		if files[i].Primary {
			return &files[i]
		}
	}
	if len(files) > 0 {
		return &files[0]
	}
	return nil
}

// serverPackFile prefers a file with "[server]" in the name for modpack installs.
// Modpack authors often include a separate server pack alongside the client pack.
// Falls back to the primary file if no server-specific file exists.
func serverPackFile(files []modrinthFile) *modrinthFile {
	for i := range files {
		if strings.Contains(strings.ToLower(files[i].Filename), "[server]") {
			return &files[i]
		}
	}
	return primaryFile(files)
}
