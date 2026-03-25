package service_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
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
	gs1 := &models.Gameserver{
		Name:          "Pre-loaded",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           []byte(`{"REQUIRED_VAR":"x"}`),
		NodeID:        strPtr("worker-1"),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	// Now create another — worker-2 has 100% headroom vs worker-1's 50%
	gs2 := &models.Gameserver{
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

func strPtr(s string) *string { return &s }

func TestPlacement_TagFiltering(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	testutil.RegisterFakeWorker(t, svc, "worker-gpu", testutil.WithTags([]string{"gpu"}))
	testutil.RegisterFakeWorker(t, svc, "worker-plain")

	// Set tags on the gpu worker in the DB
	_, err := svc.DB.Exec(`UPDATE worker_nodes SET tags = ? WHERE id = ?`, `["gpu"]`, "worker-gpu")
	require.NoError(t, err)

	// Request a gameserver that requires the "gpu" tag
	gs := &models.Gameserver{
		Name:     "GPU Server",
		GameID:   testutil.TestGameID,
		NodeTags: `["gpu"]`,
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs)
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

	gs := &models.Gameserver{
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
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithPortRange(27000, 27010))

	gs := &models.Gameserver{
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
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithPortRange(27000, 27001))

	gs1 := &models.Gameserver{
		Name:     "First",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		Env:      []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	gs2 := &models.Gameserver{
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

	gs := &models.Gameserver{
		Name:          "Too Big",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 2048,
		Env:           []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err, "should reject gameserver exceeding node capacity")
}
