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
		svc.Dispatcher,
		svc.Registry,
		nil, // restartFunc not needed for recovery tests
		log,
	)
	svc.GameserverSvc.SetStatusProvider(sm)
	return sm
}

func TestRecovery_RunningInDB_InstanceGone(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver so it gets a real instance
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))
	svc.GameserverSvc.WaitForOperations()

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.InstanceID)

	// Set status to "running" via activity to simulate a crash recovery scenario
	s := store.New(svc.DB)
	testutil.SetGameserverDesiredState(t, s, gs.ID, "running")

	// Remove the instance from the fake worker so InspectInstance fails
	fw.FailNext("InspectInstance", fmt.Errorf("instance not found"))

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

func TestRecovery_RunningInDB_InstanceRunning(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Set up state directly to avoid race with StatusSubscriber processing
	// lifecycle events from a real Start() call.
	instanceID := fw.AddFakeInstance(gs.ID)
	s := store.New(svc.DB)
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.InstanceID = &instanceID
	require.NoError(t, s.UpdateGameserver(fetched))
	testutil.SetGameserverDesiredState(t, s, gs.ID, "running")

	// Instance is "running" in fake worker — recovery should re-attach (set to "started")
	sm := newTestStatusManager(t, svc)
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	recovered, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	// Recovery populates worker state cache with running — DeriveStatus returns "running"
	assert.Equal(t, controller.StatusRunning, recovered.Status)
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
		PortMode:    "auto",
		NodeID:      &nodeID,
		NodeTags:    model.Labels{},
		AutoRestart: &autoRestart,
	}
	require.NoError(t, s.CreateGameserver(gs))
	// Gameserver wants to be running but worker is offline — DeriveStatus returns "unreachable"
	testutil.SetGameserverDesiredState(t, s, "gs-unreachable", "running")

	sm := newTestStatusManager(t, svc)

	// Should not crash — worker is offline, so DeriveStatus returns "unreachable"
	require.NoError(t, sm.RecoverOnStartup(context.Background()))

	recovered, err := svc.GameserverSvc.GetGameserver("gs-unreachable")
	require.NoError(t, err)
	assert.Equal(t, controller.StatusUnreachable, recovered.Status)
}
