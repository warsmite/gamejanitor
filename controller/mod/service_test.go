package mod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

func TestAvailableCategories_NoLoader(t *testing.T) {
	t.Parallel()
	svc := &ModService{}
	game := &games.Game{
		Mods: games.ModsConfig{
			Categories: []games.ModCategoryDef{
				{Name: "Mods", Sources: []games.ModCategorySource{{Name: "modrinth"}}},
				{Name: "Maps", Sources: []games.ModCategorySource{{Name: "workshop"}}},
			},
		},
	}

	// No loader config = all categories available
	cats := svc.availableCategories(game, model.Env{})
	assert.Len(t, cats, 2)
}

func TestAvailableCategories_LoaderFilters(t *testing.T) {
	t.Parallel()
	svc := &ModService{}
	game := &games.Game{
		Mods: games.ModsConfig{
			Loader: &games.ModLoaderDef{
				Env: "MODLOADER",
				Options: map[string]games.ModLoaderOption{
					"vanilla": {ModSources: []string{}},
					"fabric":  {ModSources: []string{"modrinth"}, LoaderID: "fabric"},
				},
			},
			Categories: []games.ModCategoryDef{
				{Name: "Mods", Sources: []games.ModCategorySource{{Name: "modrinth"}}},
				{Name: "Maps", Sources: []games.ModCategorySource{{Name: "workshop"}}},
			},
		},
	}

	// Vanilla = no sources allowed → no categories with matching sources
	cats := svc.availableCategories(game, model.Env{"MODLOADER": "vanilla"})
	assert.Len(t, cats, 0)

	// Fabric = modrinth allowed → "Mods" category available, "Maps" not (workshop not allowed)
	cats = svc.availableCategories(game, model.Env{"MODLOADER": "fabric"})
	assert.Len(t, cats, 1)
	assert.Equal(t, "Mods", cats[0].Name)
}

func TestAvailableCategories_FrameworkToggle(t *testing.T) {
	t.Parallel()
	svc := &ModService{}
	game := &games.Game{
		Mods: games.ModsConfig{
			Loader: &games.ModLoaderDef{
				Env: "OXIDE_ENABLED",
				Options: map[string]games.ModLoaderOption{
					"false": {ModSources: []string{}},
					"true":  {ModSources: []string{"umod"}},
				},
			},
			Categories: []games.ModCategoryDef{
				{Name: "Plugins", Sources: []games.ModCategorySource{{Name: "umod"}}},
			},
		},
	}

	// Oxide disabled
	cats := svc.availableCategories(game, model.Env{"OXIDE_ENABLED": "false"})
	assert.Len(t, cats, 0)

	// Oxide enabled
	cats = svc.availableCategories(game, model.Env{"OXIDE_ENABLED": "true"})
	assert.Len(t, cats, 1)
	assert.Equal(t, "Plugins", cats[0].Name)
}

func TestResolveLoaderID(t *testing.T) {
	t.Parallel()
	svc := &ModService{}

	game := &games.Game{
		Mods: games.ModsConfig{
			Loader: &games.ModLoaderDef{
				Env: "MODLOADER",
				Options: map[string]games.ModLoaderOption{
					"vanilla": {LoaderID: ""},
					"fabric":  {LoaderID: "fabric"},
					"forge":   {LoaderID: "forge"},
				},
			},
		},
	}

	assert.Equal(t, "", svc.resolveLoaderID(game, model.Env{"MODLOADER": "vanilla"}))
	assert.Equal(t, "fabric", svc.resolveLoaderID(game, model.Env{"MODLOADER": "fabric"}))
	assert.Equal(t, "forge", svc.resolveLoaderID(game, model.Env{"MODLOADER": "forge"}))
	assert.Equal(t, "", svc.resolveLoaderID(game, model.Env{"MODLOADER": "unknown"}))
}

func TestResolveLoaderID_NoLoader(t *testing.T) {
	t.Parallel()
	svc := &ModService{}
	game := &games.Game{}
	assert.Equal(t, "", svc.resolveLoaderID(game, model.Env{}))
}

func TestBuildFilters_MergesConfigAndFilters(t *testing.T) {
	t.Parallel()
	svc := &ModService{}

	src := games.ModCategorySource{
		Name: "modrinth",
		Filters: map[string]string{
			"project_type": "mod",
		},
		Config: map[string]string{
			"some_config": "value",
		},
	}

	filters := svc.buildFilters(src, "1.21.1", "fabric")
	assert.Equal(t, "1.21.1", filters.GameVersion)
	assert.Equal(t, "fabric", filters.Loader)
	assert.Equal(t, "mod", filters.Extra["project_type"])
	assert.Equal(t, "value", filters.Extra["some_config"])
}

func TestSanitizeFileName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "mod.jar", sanitizeFileName("mod.jar"))
	assert.Equal(t, "mod.jar", sanitizeFileName("/path/to/mod.jar"))
	assert.Equal(t, "mod.jar", sanitizeFileName("../../../mod.jar"))
	assert.Equal(t, "mod.jar", sanitizeFileName("path\\to\\mod.jar"))
	assert.Equal(t, "", sanitizeFileName(""))
	assert.Equal(t, "", sanitizeFileName(".."))
	assert.Equal(t, "", sanitizeFileName("/"))
}

func TestFindSource(t *testing.T) {
	t.Parallel()

	cat := &games.ModCategoryDef{
		Name: "Mods",
		Sources: []games.ModCategorySource{
			{Name: "modrinth", Delivery: "file"},
			{Name: "curseforge", Delivery: "file"},
		},
	}

	src := findSource(cat, "modrinth")
	assert.NotNil(t, src)
	assert.Equal(t, "modrinth", src.Name)

	src = findSource(cat, "nonexistent")
	assert.Nil(t, src)
}
