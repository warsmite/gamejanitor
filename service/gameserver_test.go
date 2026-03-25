package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestGameserver_Create_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{
		Name:   "My Test Server",
		GameID: testutil.TestGameID,
		Env:    []byte(`{"REQUIRED_VAR":"hello"}`),
	}

	sftpPassword, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	assert.NotEmpty(t, sftpPassword, "should return SFTP password")
	assert.NotEmpty(t, gs.ID, "should assign an ID")
	assert.NotEmpty(t, gs.VolumeName, "should assign a volume name")
	assert.Equal(t, "stopped", gs.Status)
	assert.NotEmpty(t, gs.SFTPUsername)

	// Verify it persisted in the DB
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "My Test Server", fetched.Name)
	assert.Equal(t, testutil.TestGameID, fetched.GameID)

	// Verify it was placed on worker-1
	require.NotNil(t, fetched.NodeID)
	assert.Equal(t, "worker-1", *fetched.NodeID)

	// Verify ports were auto-allocated
	assert.NotEqual(t, "[]", string(fetched.Ports))
	assert.NotEqual(t, "null", string(fetched.Ports))
}

func TestGameserver_Create_InvalidGameID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{
		Name:   "Bad Game",
		GameID: "nonexistent-game",
	}

	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGameserver_Create_MissingRequiredEnvVar(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{
		Name:   "Missing Env",
		GameID: testutil.TestGameID,
		// REQUIRED_VAR is not set
	}

	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Required Variable")
}

func TestGameserver_Create_NoWorkersAvailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	// No workers registered
	ctx := testutil.TestContext()

	gs := &models.Gameserver{
		Name:   "No Workers",
		GameID: testutil.TestGameID,
		Env:    []byte(`{"REQUIRED_VAR":"hello"}`),
	}

	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workers available")
}

func TestGameserver_Delete_CascadesCleanup(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{
		Name:   "To Delete",
		GameID: testutil.TestGameID,
		Env:    []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Verify volume exists
	assert.True(t, fw.VolumeExists(gs.VolumeName))

	err = svc.GameserverSvc.DeleteGameserver(ctx, gs.ID)
	require.NoError(t, err)

	// Verify gameserver is gone from DB
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched)

	// Verify volume was removed
	assert.False(t, fw.VolumeExists(gs.VolumeName))
}

func TestGameserver_Create_EventPublished(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Subscribe before creating so we catch the event
	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	gs := &models.Gameserver{
		Name:   "Event Test",
		GameID: testutil.TestGameID,
		Env:    []byte(`{"REQUIRED_VAR":"hello"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Drain events and find the create event
	found := false
	for i := 0; i < 10; i++ {
		select {
		case evt := <-ch:
			if evt.EventType() == service.EventGameserverCreate {
				found = true
				gsEvt, ok := evt.(service.GameserverEvent)
				assert.True(t, ok)
				assert.Equal(t, gs.ID, gsEvt.GameserverID)
				assert.Equal(t, testutil.TestGameID, gsEvt.GameID)
			}
		default:
			// no more events
			i = 10
		}
	}
	assert.True(t, found, "expected gameserver.create event to be published")
}
