package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestGameserver_Create_InvalidGameID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Bad Game",
		GameID: "nonexistent-game",
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGameserver_Create_MissingRequiredEnvVar(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// test-game requires REQUIRED_VAR — omit it
	gs := &model.Gameserver{
		Name:   "Missing Env",
		GameID: testutil.TestGameID,
		Env:    model.Env{},
	}
	_, err := svc.Manager.Create(ctx, gs)
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
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.Error(t, err)
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
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Drain events looking for gameserver.create
	found := false
	for i := 0; i < 10; i++ {
		select {
		case evt := <-ch:
			if evt.EventType() == event.EventGameserverCreate {
				found = true
			}
		default:
		}
		if found {
			break
		}
	}
	assert.True(t, found, "should have received a gameserver.create event")
}
