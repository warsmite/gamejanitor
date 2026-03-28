package store_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func setupModTest(t *testing.T) (*sql.DB, *store.ModStore) {
	t.Helper()
	db := testutil.NewTestDB(t)

	// Create a gameserver for FK satisfaction
	autoRestart := false
	s := store.New(db)
	require.NoError(t, s.CreateGameserver(&model.Gameserver{
		ID: "gs-1", Name: "test", GameID: "test-game",
		VolumeName: "vol-1", AutoRestart: &autoRestart,
	}))

	return db, store.NewModStore(db)
}

func TestModStore_CRUD(t *testing.T) {
	t.Parallel()
	_, s := setupModTest(t)

	mod := &model.InstalledMod{
		ID:           "mod-1",
		GameserverID: "gs-1",
		Source:       "modrinth",
		SourceID:     "lithium",
		Category:     "Mods",
		Name:         "Lithium",
		Version:      "0.13.1",
		VersionID:    "v-123",
		FilePath:     "/data/mods/lithium-0.13.1.jar",
		FileName:     "lithium-0.13.1.jar",
		Delivery:     "file",
		Metadata:     json.RawMessage(`{}`),
		InstalledAt:  time.Now(),
	}

	require.NoError(t, s.CreateInstalledMod(mod))

	got, err := s.GetInstalledMod("mod-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Lithium", got.Name)
	assert.Equal(t, "Mods", got.Category)
	assert.Equal(t, "file", got.Delivery)
	assert.False(t, got.AutoInstalled)

	got, err = s.GetInstalledModBySource("gs-1", "modrinth", "lithium")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "mod-1", got.ID)

	mods, err := s.ListInstalledMods("gs-1")
	require.NoError(t, err)
	assert.Len(t, mods, 1)

	require.NoError(t, s.DeleteInstalledMod("mod-1"))
	got, err = s.GetInstalledMod("mod-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestModStore_AutoInstalledAndDependsOn(t *testing.T) {
	t.Parallel()
	_, s := setupModTest(t)

	parentID := "mod-parent"
	depID := "mod-dep"

	parent := &model.InstalledMod{
		ID: parentID, GameserverID: "gs-1", Source: "modrinth", SourceID: "lithium",
		Category: "Mods", Name: "Lithium", Delivery: "file",
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(parent))

	dep := &model.InstalledMod{
		ID: depID, GameserverID: "gs-1", Source: "modrinth", SourceID: "fabric-api",
		Category: "Mods", Name: "Fabric API", Delivery: "file",
		AutoInstalled: true, DependsOn: &parentID,
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(dep))

	got, err := s.GetInstalledMod(depID)
	require.NoError(t, err)
	assert.True(t, got.AutoInstalled)
	require.NotNil(t, got.DependsOn)
	assert.Equal(t, parentID, *got.DependsOn)
}

func TestModStore_PackID(t *testing.T) {
	t.Parallel()
	_, s := setupModTest(t)

	packID := "pack-atm10"
	pack := &model.InstalledMod{
		ID: packID, GameserverID: "gs-1", Source: "modrinth", SourceID: "atm10",
		Category: "Modpacks", Name: "ATM10", Delivery: "pack",
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(pack))

	mod1 := &model.InstalledMod{
		ID: "mod-from-pack-1", GameserverID: "gs-1", Source: "modrinth", SourceID: "jei",
		Category: "Mods", Name: "JEI", Delivery: "file", PackID: &packID,
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	mod2 := &model.InstalledMod{
		ID: "mod-from-pack-2", GameserverID: "gs-1", Source: "modrinth", SourceID: "waystones",
		Category: "Mods", Name: "Waystones", Delivery: "file", PackID: &packID,
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(mod1))
	require.NoError(t, s.CreateInstalledMod(mod2))

	packMods, err := s.ListModsByPackID("gs-1", packID)
	require.NoError(t, err)
	assert.Len(t, packMods, 2)

	// SetModPackID on a standalone mod
	standalone := &model.InstalledMod{
		ID: "mod-standalone", GameserverID: "gs-1", Source: "modrinth", SourceID: "sodium",
		Category: "Mods", Name: "Sodium", Delivery: "file",
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(standalone))
	require.NoError(t, s.SetModPackID("mod-standalone", packID))

	packMods, err = s.ListModsByPackID("gs-1", packID)
	require.NoError(t, err)
	assert.Len(t, packMods, 3)
}

func TestModStore_PackExclusions(t *testing.T) {
	t.Parallel()
	_, s := setupModTest(t)

	packID := "pack-1"
	pack := &model.InstalledMod{
		ID: packID, GameserverID: "gs-1", Source: "modrinth", SourceID: "atm10",
		Category: "Modpacks", Name: "ATM10", Delivery: "pack",
		Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(pack))

	exclusions, err := s.GetPackExclusions(packID)
	require.NoError(t, err)
	assert.Empty(t, exclusions)

	require.NoError(t, s.CreatePackExclusion(&model.PackExclusion{
		PackModID:  packID,
		SourceID:   "jei",
		ExcludedAt: time.Now(),
	}))

	exclusions, err = s.GetPackExclusions(packID)
	require.NoError(t, err)
	assert.True(t, exclusions["jei"])
	assert.False(t, exclusions["waystones"])

	// Duplicate exclusion should not error
	require.NoError(t, s.CreatePackExclusion(&model.PackExclusion{
		PackModID:  packID,
		SourceID:   "jei",
		ExcludedAt: time.Now(),
	}))
}

func TestModStore_UpdateModVersion(t *testing.T) {
	t.Parallel()
	_, s := setupModTest(t)

	mod := &model.InstalledMod{
		ID: "mod-1", GameserverID: "gs-1", Source: "modrinth", SourceID: "lithium",
		Category: "Mods", Name: "Lithium", Version: "0.12.0", VersionID: "v-old",
		Delivery: "file", Metadata: json.RawMessage(`{}`), InstalledAt: time.Now(),
	}
	require.NoError(t, s.CreateInstalledMod(mod))

	require.NoError(t, s.UpdateModVersion("mod-1", "v-new", "0.13.1"))

	got, err := s.GetInstalledMod("mod-1")
	require.NoError(t, err)
	assert.Equal(t, "v-new", got.VersionID)
	assert.Equal(t, "0.13.1", got.Version)
}
