package gameserver_test

import (
	"github.com/warsmite/gamejanitor/controller"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestGameserver_Create_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "My Test Server",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "hello"},
	}

	sftpPassword, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	assert.NotEmpty(t, sftpPassword, "should return SFTP password")
	assert.NotEmpty(t, gs.ID, "should assign an ID")
	assert.NotEmpty(t, gs.VolumeName, "should assign a volume name")
	assert.Equal(t, "stopped", gs.DesiredState)
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
	assert.NotEmpty(t, fetched.Ports)
	assert.NotNil(t, fetched.Ports)
}

func TestGameserver_Create_InvalidGameID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
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

	gs := &model.Gameserver{
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

	gs := &model.Gameserver{
		Name:   "No Workers",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "hello"},
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

	gs := &model.Gameserver{
		Name:   "To Delete",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "hello"},
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

	gs := &model.Gameserver{
		Name:   "Event Test",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "hello"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// CreateGameserver publishes synchronously, so the event is already in the channel.
	// Drain buffered events — break on first empty read since Publish is non-blocking.
	found := false
	for {
		select {
		case evt := <-ch:
			if evt.EventType() == controller.EventGameserverCreate {
				found = true
				gsEvt, ok := evt.(controller.GameserverActionEvent)
				assert.True(t, ok)
				assert.Equal(t, gs.ID, gsEvt.GameserverID)
				assert.NotNil(t, gsEvt.Gameserver)
				assert.Equal(t, testutil.TestGameID, gsEvt.Gameserver.GameID)
			}
		default:
			goto done
		}
	}
done:
	assert.True(t, found, "expected gameserver.create event to be published")
}
