package gameserver_test

import (
	"github.com/warsmite/gamejanitor/controller/settings"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestPlacement_RanksWorkersByHeadroom(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Worker-1: 4GB memory, worker-2: 8GB memory
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(4096))
	testutil.RegisterFakeWorker(t, svc, "worker-2", testutil.WithMaxMemoryMB(8192))

	// Pre-load worker-1 with a 2GB gameserver to reduce its headroom to 50%
	gs1 := &model.Gameserver{
		Name:          "Pre-loaded",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           []byte(`{"REQUIRED_VAR":"x"}`),
		NodeID:        testutil.StrPtr("worker-1"),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	// Now create another — worker-2 has 100% headroom vs worker-1's 50%
	gs2 := &model.Gameserver{
		Name:          "Placement Test",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 1024,
		Env:           []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)
	require.NotNil(t, gs2.NodeID)
	assert.Equal(t, "worker-2", *gs2.NodeID)
}

func TestPlacement_TagFiltering(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	testutil.RegisterFakeWorker(t, svc, "worker-gpu", testutil.WithTags(model.Labels{"hardware": "gpu"}))
	testutil.RegisterFakeWorker(t, svc, "worker-plain")

	// Request a gameserver that requires the "hardware=gpu" label
	gs := &model.Gameserver{
		Name:     "GPU Server",
		GameID:   testutil.TestGameID,
		NodeTags: model.Labels{"hardware": "gpu"},
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	require.NotNil(t, gs.NodeID)
	assert.Equal(t, "worker-gpu", *gs.NodeID)
}

func TestPlacement_CordonedWorkerSkipped(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	testutil.RegisterFakeWorker(t, svc, "worker-cordoned")
	testutil.RegisterFakeWorker(t, svc, "worker-active")

	// Cordon worker-cordoned
	_, err := svc.DB.Exec(`UPDATE worker_nodes SET cordoned = 1 WHERE id = ?`, "worker-cordoned")
	require.NoError(t, err)

	gs := &model.Gameserver{
		Name:   "Cordon Test",
		GameID: testutil.TestGameID,
		Env:    []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	require.NotNil(t, gs.NodeID)
	assert.Equal(t, "worker-active", *gs.NodeID)
}

func TestPortAllocation_ContiguousBlock(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Small port range: 27000-27010, test game needs 2 ports
		svc.SettingsSvc.Set(settings.SettingPortRangeStart, 27000)
	svc.SettingsSvc.Set(settings.SettingPortRangeEnd, 27010)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := &model.Gameserver{
		Name:     "Port Test",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Parse the allocated ports
	var ports []map[string]any
	err = json.Unmarshal(gs.Ports, &ports)
	require.NoError(t, err)
	assert.Len(t, ports, 2, "test game has 2 ports")

	// Verify ports are within the range
	for _, p := range ports {
		hostPort := int(p["host_port"].(float64))
		assert.GreaterOrEqual(t, hostPort, 27000)
		assert.LessOrEqual(t, hostPort, 27010)
	}
}

func TestPortAllocation_Exhaustion(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Tiny range: only 2 ports, test game needs 2 — first create succeeds, second fails
		svc.SettingsSvc.Set(settings.SettingPortRangeStart, 27000)
	svc.SettingsSvc.Set(settings.SettingPortRangeEnd, 27001)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs1 := &model.Gameserver{
		Name:     "First",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	gs2 := &model.Gameserver{
		Name:     "Second",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.Error(t, err, "should fail when ports are exhausted")
}

func TestPlacement_CapacityOverflow(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Worker with only 1GB memory
	testutil.RegisterFakeWorker(t, svc, "worker-small", testutil.WithMaxMemoryMB(1024))

	gs := &model.Gameserver{
		Name:          "Too Big",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err, "should reject gameserver exceeding node capacity")
}

func TestPortAllocation_PortsFreedOnDelete(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Only 2 ports available — test game needs exactly 2
		svc.SettingsSvc.Set(settings.SettingPortRangeStart, 27000)
	svc.SettingsSvc.Set(settings.SettingPortRangeEnd, 27001)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs1 := &model.Gameserver{
		Name: "First", GameID: testutil.TestGameID, PortMode: "auto",
		Env: []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	// Range is full — second create should fail
	gs2 := &model.Gameserver{
		Name: "Second", GameID: testutil.TestGameID, PortMode: "auto",
		Env: []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.Error(t, err, "range should be full")

	// Delete first gameserver — frees the ports
	require.NoError(t, svc.GameserverSvc.DeleteGameserver(ctx, gs1.ID))

	// Now the same range should work again
	gs3 := &model.Gameserver{
		Name: "Third", GameID: testutil.TestGameID, PortMode: "auto",
		Env: []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs3)
	require.NoError(t, err, "ports should be reusable after delete")
}

func TestPortAllocation_MultipleGameserversFillRange(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// Range of 10 ports, test game needs 2 each — should fit 5 gameservers
		svc.SettingsSvc.Set(settings.SettingPortRangeStart, 27000)
	svc.SettingsSvc.Set(settings.SettingPortRangeEnd, 27009)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	allPorts := make(map[int]bool)
	for i := 0; i < 5; i++ {
		gs := &model.Gameserver{
			Name: "Fill-" + string(rune('A'+i)), GameID: testutil.TestGameID, PortMode: "auto",
			Env: []byte(`{"REQUIRED_VAR":"v"}`),
		}
		_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
		require.NoError(t, err, "gameserver %d should fit", i)

		var ports []map[string]any
		require.NoError(t, json.Unmarshal(gs.Ports, &ports))
		for _, p := range ports {
			hp := int(p["host_port"].(float64))
			assert.False(t, allPorts[hp], "port %d allocated twice", hp)
			allPorts[hp] = true
		}
	}
	assert.Len(t, allPorts, 10, "all 10 ports in range should be allocated")

	// 6th should fail — range exhausted
	gs6 := &model.Gameserver{
		Name: "Overflow", GameID: testutil.TestGameID, PortMode: "auto",
		Env: []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs6)
	require.Error(t, err)
}

// TestPortAllocation_ConcurrentCreates verifies that the placement mutex
// prevents duplicate port allocation when multiple goroutines create
// gameservers simultaneously.
func TestPortAllocation_ConcurrentCreates(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	// 20 ports, 2 per gameserver = room for 10. Launch 10 goroutines.
		svc.SettingsSvc.Set(settings.SettingPortRangeStart, 27000)
	svc.SettingsSvc.Set(settings.SettingPortRangeEnd, 27019)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	gameservers := make([]*model.Gameserver, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gs := &model.Gameserver{
				Name: "Concurrent", GameID: testutil.TestGameID, PortMode: "auto",
				Env: []byte(`{"REQUIRED_VAR":"v"}`),
			}
			_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
			errs[idx] = err
			if err == nil {
				gameservers[idx] = gs
			}
		}(i)
	}
	wg.Wait()

	// All 10 should succeed — we have exactly enough ports
	successCount := 0
	allPorts := make(map[int]bool)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			continue
		}
		successCount++
		var ports []map[string]any
		require.NoError(t, json.Unmarshal(gameservers[i].Ports, &ports))
		for _, p := range ports {
			hp := int(p["host_port"].(float64))
			assert.False(t, allPorts[hp], "port %d allocated to multiple gameservers", hp)
			allPorts[hp] = true
		}
	}
	assert.Equal(t, n, successCount, "all concurrent creates should succeed")
	assert.Len(t, allPorts, n*2, "each gameserver uses 2 ports, all should be unique")
}
