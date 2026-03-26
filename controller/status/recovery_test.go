package status_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// newTestStatusManager creates a StatusManager wired to the test ServiceBundle.
func newTestStatusManager(t *testing.T, svc *testutil.ServiceBundle) *status.StatusManager {
	t.Helper()
	s := store.New(svc.DB)
	log := testutil.TestLogger()
	sm := status.NewStatusManager(
		s,
		svc.Broadcaster,
		svc.QuerySvc,
		svc.StatsPoller,
		svc.ReadyWatcher,
		svc.Dispatcher,
		svc.Registry,
		nil, // restartFunc not needed for recovery tests
		log,
	)
	return sm
}

func TestRecovery_RunningInDB_ContainerGone(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver so it gets a real container
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.ContainerID)

	// Set status to "running" in DB to simulate a crash recovery scenario
	s := store.New(svc.DB)
	fetched.Status = controller.StatusRunning
	require.NoError(t, s.UpdateGameserver(fetched))

	// Remove the container from the fake worker so InspectContainer fails
	fw.FailNext("InspectContainer", fmt.Errorf("container not found"))

	sm := newTestStatusManager(t, svc)
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	recovered, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, controller.StatusStopped, recovered.Status)
}

func TestRecovery_StoppedInDB_NoAction(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Gameserver starts as "stopped" — recovery should leave it alone
	sm := newTestStatusManager(t, svc)
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, controller.StatusStopped, fetched.Status)
}

func TestRecovery_RunningInDB_ContainerRunning(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver so it gets a container
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Set status to "running" in DB
	s := store.New(svc.DB)
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.Status = controller.StatusRunning
	require.NoError(t, s.UpdateGameserver(fetched))

	// Container is "running" in fake worker — recovery should re-attach (set to "started")
	sm := newTestStatusManager(t, svc)
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	recovered, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	// Recovery sets running containers to "started" and re-attaches the ready watcher
	assert.Equal(t, controller.StatusStarted, recovered.Status)
}

func TestRecovery_UnreachableStatus_WorkerOffline(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Create a gameserver with a node_id but don't register any worker
	s := store.New(svc.DB)
	nodeID := "offline-worker"
	// Insert the worker node record so the gameserver can reference it
	_, err := svc.DB.Exec(`INSERT INTO worker_nodes (id) VALUES (?)`, nodeID)
	require.NoError(t, err)

	autoRestart := false
	gs := &model.Gameserver{
		ID:          "gs-unreachable",
		Name:        "Unreachable GS",
		GameID:      testutil.TestGameID,
		Ports:       model.Ports{},
		Env:         model.Env{"REQUIRED_VAR": "v"},
		VolumeName:  "vol-unreachable",
		Status:      controller.StatusUnreachable,
		PortMode:    "auto",
		NodeID:      &nodeID,
		NodeTags:    model.Labels{},
		AutoRestart: &autoRestart,
	}
	require.NoError(t, s.CreateGameserver(gs))

	sm := newTestStatusManager(t, svc)

	// Should not crash — unreachable status doesn't need recovery (NeedsRecovery returns false)
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	fetched, err := s.GetGameserver("gs-unreachable")
	require.NoError(t, err)
	// Unreachable doesn't satisfy NeedsRecovery, so status is unchanged
	assert.Equal(t, controller.StatusUnreachable, fetched.Status)
}
