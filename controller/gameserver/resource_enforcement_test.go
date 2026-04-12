package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestResourceEnforcement_MemoryExceedsNodeLimit(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(1024))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Too Much Memory",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory limit")
}

func TestResourceEnforcement_CPUExceedsNodeLimit(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxCPU(2.0))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:     "Too Much CPU",
		GameID:   testutil.TestGameID,
		CPULimit: 4.0,
		Env:      model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CPU limit")
}

func TestResourceEnforcement_CumulativeMemoryExceedsLimit(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(3000))
	ctx := testutil.TestContext()

	// First gameserver uses 2048MB — fits
	gs1 := &model.Gameserver{
		Name:          "First",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs1)
	require.NoError(t, err)

	// Second gameserver wants 2048MB — cumulative 4096 > 3000 limit
	gs2 := &model.Gameserver{
		Name:          "Second",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err = svc.Manager.Create(ctx, gs2)
	require.Error(t, err, "cumulative allocation should exceed node limit")
}

func TestResourceEnforcement_RequireMemoryLimitSetting(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Enable the require_memory_limit setting
	require.NoError(t, svc.SettingsSvc.Set(settings.SettingRequireMemoryLimit, true))

	gs := &model.Gameserver{
		Name:          "No Memory Set",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 0,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory_limit_mb must be > 0")
}

func TestResourceEnforcement_RequireCPULimitSetting(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	require.NoError(t, svc.SettingsSvc.Set(settings.SettingRequireCPULimit, true))

	// CPU has no game default, so zero stays zero
	gs := &model.Gameserver{
		Name:     "No CPU Set",
		GameID:   testutil.TestGameID,
		CPULimit: 0,
		Env:      model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cpu_limit must be > 0")
}

func TestResourceEnforcement_RequireStorageLimitSetting(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	require.NoError(t, svc.SettingsSvc.Set(settings.SettingRequireStorageLimit, true))

	gs := &model.Gameserver{
		Name:   "No Storage Set",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage_limit_mb must be > 0")
}

func TestResourceEnforcement_ZeroMemoryMeansUnlimited(t *testing.T) {
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Unlimited Memory",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 0,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, fetched.MemoryLimitMB, "0 should mean unlimited, not overridden to recommended")
}
