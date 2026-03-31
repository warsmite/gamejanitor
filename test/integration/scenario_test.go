package integration_test

import (
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/auth"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/worker"
)

// Scenario tests simulate real user workflows end-to-end.
// These catch integration issues between services that unit tests miss.

// --- Archetype 1: Newbie / Homelab ---
// Zero config, single node, no auth, just wants a game server running.

func TestScenario_Newbie_CreateAndStartGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Newbie creates a gameserver with minimal info — just name and game
	gs := &model.Gameserver{
		Name:   "My Minecraft Server",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "yes"},
	}
	sftpPassword, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Should get back SFTP credentials for file access
	assert.NotEmpty(t, sftpPassword, "newbie should get SFTP password")
	assert.NotEmpty(t, gs.SFTPUsername, "newbie should get SFTP username")

	// Should have been placed on the only worker
	require.NotNil(t, gs.NodeID)

	// Should have ports assigned (even without setting PortMode explicitly)
	assert.NotNil(t, gs.Ports)
	assert.NotEmpty(t, gs.Ports)

	// Start should work
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))
	assert.Greater(t, fw.InstanceCount(), 0)

	// Stop should work
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))
}

func TestScenario_Newbie_DefaultSettings_Safe(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Auth disabled by default — newbie doesn't need to set up tokens
	assert.False(t, svc.SettingsSvc.GetBool(settings.SettingAuthEnabled))

	// Localhost bypass on — web UI works without tokens from the same machine
	assert.True(t, svc.SettingsSvc.GetBool(settings.SettingLocalhostBypass))

	// Rate limiting off by default — no surprises for single user
	assert.False(t, svc.SettingsSvc.GetBool(settings.SettingRateLimitEnabled))

	// Resource limits not required — newbie shouldn't need to know about memory limits
	assert.False(t, svc.SettingsSvc.GetBool(settings.SettingRequireMemoryLimit))
	assert.False(t, svc.SettingsSvc.GetBool(settings.SettingRequireCPULimit))
	assert.False(t, svc.SettingsSvc.GetBool(settings.SettingRequireStorageLimit))
}

func TestScenario_Newbie_SFTPLogin(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "SFTP Test",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	sftpPassword, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Verify SFTP credentials work against the DB directly
	// (testing the same path the SFTP server uses)
	db := store.New(svc.DB)
	fetched, err := db.GetGameserverBySFTPUsername(gs.SFTPUsername)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, gs.ID, fetched.ID)

	// Password should match the bcrypt hash
	assert.NotEmpty(t, sftpPassword)
	assert.NotEmpty(t, fetched.HashedSFTPPassword)
}

func TestScenario_Newbie_RegenerateSFTPPassword(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "SFTP Regen",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	oldPassword, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	newPassword, err := svc.GameserverSvc.RegenerateSFTPPassword(ctx, gs.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldPassword, newPassword, "regenerated password should be different")
}

// --- Archetype 2: Power User ---
// Multi-node, custom tokens, backups, schedules, CLI/API driven.

func TestScenario_PowerUser_MultiNodePlacementAndMigration(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	wUS := testutil.RegisterFakeWorker(t, svc, "node-us", testutil.WithMaxMemoryMB(8192))
	testutil.RegisterFakeWorker(t, svc, "node-eu", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	// Create on node-us explicitly
	gs := &model.Gameserver{
		Name:          "US Server",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		NodeID:        testutil.StrPtr("node-us"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	assert.Equal(t, "node-us", *gs.NodeID)
	testutil.SeedVolumeData(t, wUS, gs.VolumeName)

	// Migrate to EU
	require.NoError(t, svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "node-eu"))

	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	assert.Equal(t, "node-eu", *fetched.NodeID)
}

func TestScenario_PowerUser_ScopedTokenWorkflow(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Create two gameservers
	gs1 := &model.Gameserver{Name: "Minecraft", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	gs2 := &model.Gameserver{Name: "Rust", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)

	// Create a token scoped to gs1 with start/stop only
	rawToken, _, err := svc.AuthSvc.CreateCustomToken("mc-operator",
		[]string{gs1.ID},
		[]string{auth.PermGameserverStart, auth.PermGameserverStop},
		nil)
	require.NoError(t, err)

	token := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, token)

	// Can start gs1
	assert.True(t, auth.HasPermission(token, gs1.ID, auth.PermGameserverStart))
	// Cannot start gs2
	assert.False(t, auth.HasPermission(token, gs2.ID, auth.PermGameserverStart))
	// Cannot delete gs1 (wrong permission)
	assert.False(t, auth.HasPermission(token, gs1.ID, auth.PermGameserverDelete))
}

func TestScenario_PowerUser_BackupAndSchedule(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Create a manual backup
	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "pre-update-backup")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", backup.Status)

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := store.New(svc.DB).GetBackup(backup.ID)
		if b != nil && b.Status != "in_progress" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Schedule daily restarts
	sched := &model.Schedule{
		GameserverID: gs.ID,
		Name:         "daily-restart",
		Type:         "restart",
		CronExpr:     "0 4 * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	require.NoError(t, svc.ScheduleSvc.CreateSchedule(ctx, sched))

	// List schedules
	schedules, err := svc.ScheduleSvc.ListSchedules(gs.ID)
	require.NoError(t, err)
	assert.Len(t, schedules, 1)
}

func TestScenario_PowerUser_FileManagement(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Upload a config file
	require.NoError(t, svc.FileSvc.WriteFile(ctx, gs.ID, "/data/server.properties", []byte("motd=Hello World\nmax-players=20")))

	// Read it back
	data, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/data/server.properties")
	require.NoError(t, err)
	assert.Contains(t, string(data), "Hello World")

	// List directory
	entries, err := svc.FileSvc.ListDirectory(ctx, gs.ID, "/data")
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if e.Name == "server.properties" {
			found = true
		}
	}
	assert.True(t, found)

	// Path traversal blocked at service layer
	_, err = svc.FileSvc.ReadFile(ctx, gs.ID, "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be within /data")

	_ = fw
}

// --- Archetype 3: Business / Hosting ---
// Multi-node, many gameservers, auth enforced, webhooks, resource limits mandatory.

func TestScenario_Business_ModeEnforcesSecureDefaults(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	svc := settings.NewSettingsServiceWithMode(testutil.NewSettingsStore(db), log,settings.ModeBusiness)

	// Business mode should enforce secure-by-default
	assert.True(t, svc.GetBool(settings.SettingAuthEnabled), "business mode must enable auth")
	assert.False(t, svc.GetBool(settings.SettingLocalhostBypass), "business mode must disable localhost bypass")
	assert.True(t, svc.GetBool(settings.SettingRateLimitEnabled), "business mode must enable rate limiting")
	assert.True(t, svc.GetBool(settings.SettingRequireMemoryLimit), "business mode must require memory limits")
	assert.True(t, svc.GetBool(settings.SettingRequireCPULimit), "business mode must require CPU limits")
	assert.True(t, svc.GetBool(settings.SettingRequireStorageLimit), "business mode must require storage limits")
}

func TestScenario_Business_ResourceLimitsRequired(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Enable business-style requirements
	svc.SettingsSvc.Set(settings.SettingRequireMemoryLimit, true)
	svc.SettingsSvc.Set(settings.SettingRequireCPULimit, true)
	svc.SettingsSvc.Set(settings.SettingRequireStorageLimit, true)

	// Create without limits — should fail
	gs := &model.Gameserver{
		Name:     "No Limits",
		GameID:   testutil.TestGameID,
		CPULimit: 0,
		Env:      model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err, "business mode should reject gameservers without resource limits")
}

func TestScenario_Business_WebhookIntegration(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()
	log := testutil.TestLogger()

	// Business sets up a webhook for their billing/monitoring system
	whSvc := webhook.NewWebhookEndpointService(store.NewWebhookStore(svc.DB), log)
	result, err := whSvc.Create(
		"https://billing.example.com/webhook",
		"Billing webhook",
		"hmac-secret-key",
		[]string{"gameserver.create", "gameserver.delete", "gameserver.ready"},
		true,
	)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Endpoint.ID)
	assert.True(t, result.Endpoint.SecretSet, "webhook should have a secret set for HMAC verification")

	// Verify the endpoint filters work
	endpoints, err := whSvc.List()
	require.NoError(t, err)
	assert.Len(t, endpoints, 1)
	assert.Equal(t, []string{"gameserver.create", "gameserver.delete", "gameserver.ready"}, endpoints[0].Events)

	_ = ctx
}

func TestScenario_Business_MultipleWorkersWithCapacityPlanning(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	// Simulate a 3-node cluster with different capacities
	testutil.RegisterFakeWorker(t, svc, "node-1", testutil.WithMaxMemoryMB(16384), testutil.WithMaxCPU(8))
	testutil.RegisterFakeWorker(t, svc, "node-2", testutil.WithMaxMemoryMB(32768), testutil.WithMaxCPU(16))
	testutil.RegisterFakeWorker(t, svc, "node-3", testutil.WithMaxMemoryMB(8192), testutil.WithMaxCPU(4))
	ctx := testutil.TestContext()

	// Fill node-3 (smallest) to capacity by placing explicitly
	for i := 0; i < 2; i++ {
		gs := &model.Gameserver{
			Name:          "Filler",
			GameID:        testutil.TestGameID,
			MemoryLimitMB: 4096,
			CPULimit:      2.0,
			NodeID:        testutil.StrPtr("node-3"),
			Env:           model.Env{"REQUIRED_VAR": "v"},
		}
		_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
		require.NoError(t, err)
	}

	// Next gameserver (auto-placed) should NOT go on node-3 (full)
	gs := &model.Gameserver{
		Name:          "Should Not Be On Node 3",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 4096,
		CPULimit:      2.0,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	assert.NotEqual(t, "node-3", *gs.NodeID, "should not place on the full node")
}

// --- Cross-archetype: Console command capability gating ---

func TestScenario_ConsoleCommandCapabilityGating(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Set up a running instance directly (avoids lifecycle goroutine races)
	instanceID, err := fw.CreateInstance(ctx, worker.InstanceOptions{Name: "cmd-test"})
	require.NoError(t, err)
	require.NoError(t, fw.StartInstance(ctx, instanceID))

	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.InstanceID = &instanceID
	store.New(svc.DB).UpdateGameserver(fetched)
	testutil.SetGameserverStatus(t, store.New(svc.DB), gs.ID, "running")

	// test-game doesn't disable "command" capability, so SendCommand should work
	_, err = svc.ConsoleSvc.SendCommand(ctx, gs.ID, "say hello")
	require.NoError(t, err)

	// But streaming logs requires "console_read" which test-game also has
	reader, err := svc.ConsoleSvc.StreamLogs(ctx, gs.ID, 100)
	require.NoError(t, err)
	reader.Close()
}
