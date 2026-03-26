package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestWebhookEndpoint_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{
		Description: "Test Endpoint",
		URL:         "https://example.com/webhook",
		Secret:      "s3cret",
		Events:      model.StringSlice{"*"},
		Enabled:     true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))
	assert.NotEmpty(t, ep.ID, "ID should be auto-generated")
	assert.False(t, ep.CreatedAt.IsZero())

	got, err := db.GetWebhookEndpoint(ep.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, ep.ID, got.ID)
	assert.Equal(t, "Test Endpoint", got.Description)
	assert.Equal(t, "https://example.com/webhook", got.URL)
	assert.Equal(t, "s3cret", got.Secret)
	assert.Equal(t, model.StringSlice{"*"}, got.Events)
	assert.True(t, got.Enabled)
}

func TestWebhookEndpoint_GetNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	got, err := db.GetWebhookEndpoint("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWebhookEndpoint_Update(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{
		Description: "Original",
		URL:         "https://example.com/original",
		Events:      model.StringSlice{"*"},
		Enabled:     true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	ep.Description = "Updated"
	ep.URL = "https://example.com/updated"
	ep.Enabled = false
	require.NoError(t, db.UpdateWebhookEndpoint(ep))

	got, err := db.GetWebhookEndpoint(ep.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Updated", got.Description)
	assert.Equal(t, "https://example.com/updated", got.URL)
	assert.False(t, got.Enabled)
}

func TestWebhookEndpoint_UpdateNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{ID: "nonexistent", URL: "https://example.com"}
	err := db.UpdateWebhookEndpoint(ep)
	require.Error(t, err)
}

func TestWebhookEndpoint_Delete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{
		URL:     "https://example.com/delete",
		Events:  model.StringSlice{"*"},
		Enabled: true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	require.NoError(t, db.DeleteWebhookEndpoint(ep.ID))

	got, err := db.GetWebhookEndpoint(ep.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWebhookEndpoint_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	err := db.DeleteWebhookEndpoint("nonexistent")
	require.Error(t, err)
}

func TestWebhookEndpoint_List(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep1 := &model.WebhookEndpoint{URL: "https://a.com", Events: model.StringSlice{"*"}, Enabled: true}
	ep2 := &model.WebhookEndpoint{URL: "https://b.com", Events: model.StringSlice{"*"}, Enabled: false}
	require.NoError(t, db.CreateWebhookEndpoint(ep1))
	require.NoError(t, db.CreateWebhookEndpoint(ep2))

	list, err := db.ListWebhookEndpoints()
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestWebhookEndpoint_ListEnabled(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep1 := &model.WebhookEndpoint{URL: "https://a.com", Events: model.StringSlice{"*"}, Enabled: true}
	ep2 := &model.WebhookEndpoint{URL: "https://b.com", Events: model.StringSlice{"*"}, Enabled: false}
	ep3 := &model.WebhookEndpoint{URL: "https://c.com", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep1))
	require.NoError(t, db.CreateWebhookEndpoint(ep2))
	require.NoError(t, db.CreateWebhookEndpoint(ep3))

	list, err := db.ListEnabledWebhookEndpoints()
	require.NoError(t, err)
	assert.Len(t, list, 2)
	for _, ep := range list {
		assert.True(t, ep.Enabled)
	}
}

// --- Webhook Delivery Tests ---

func TestWebhookDelivery_CreateAndListByEndpoint(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d := &model.WebhookDelivery{
		ID:                "del-1",
		WebhookEndpointID: ep.ID,
		EventType:         "gameserver.started",
		Payload:           `{"id":"gs-1"}`,
		NextAttemptAt:     now,
		CreatedAt:         now,
	}
	require.NoError(t, db.CreateWebhookDelivery(d))

	list, err := db.ListDeliveriesByEndpoint(ep.ID, "", 100)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "del-1", list[0].ID)
	assert.Equal(t, model.WebhookStatePending, list[0].State)
	assert.Equal(t, 0, list[0].Attempts)
}

func TestWebhookDelivery_ListByEndpoint_FilterByState(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d1 := &model.WebhookDelivery{ID: "del-p", WebhookEndpointID: ep.ID, EventType: "a", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d1))

	d2 := &model.WebhookDelivery{ID: "del-d", WebhookEndpointID: ep.ID, EventType: "b", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d2))
	require.NoError(t, db.MarkDeliverySuccess("del-d"))

	pending, err := db.ListDeliveriesByEndpoint(ep.ID, model.WebhookStatePending, 100)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "del-p", pending[0].ID)

	delivered, err := db.ListDeliveriesByEndpoint(ep.ID, model.WebhookStateDelivered, 100)
	require.NoError(t, err)
	assert.Len(t, delivered, 1)
	assert.Equal(t, "del-d", delivered[0].ID)
}

func TestWebhookDelivery_MarkSuccess(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-suc", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d))

	require.NoError(t, db.MarkDeliverySuccess("del-suc"))

	list, err := db.ListDeliveriesByEndpoint(ep.ID, model.WebhookStateDelivered, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, model.WebhookStateDelivered, list[0].State)
	assert.Equal(t, 1, list[0].Attempts)
}

func TestWebhookDelivery_MarkRetry(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-retry", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d))

	nextAttempt := now.Add(5 * time.Minute)
	require.NoError(t, db.MarkDeliveryRetry("del-retry", nextAttempt, "connection refused"))

	list, err := db.ListDeliveriesByEndpoint(ep.ID, model.WebhookStatePending, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, 1, list[0].Attempts)
	assert.Equal(t, "connection refused", list[0].LastError)
}

func TestWebhookDelivery_MarkFailed(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-fail", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d))

	require.NoError(t, db.MarkDeliveryFailed("del-fail", "max retries exceeded"))

	list, err := db.ListDeliveriesByEndpoint(ep.ID, model.WebhookStateFailed, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, model.WebhookStateFailed, list[0].State)
	assert.Equal(t, "max retries exceeded", list[0].LastError)
}

func TestWebhookDelivery_CascadeOnEndpointDelete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-cas", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d))

	// webhook_deliveries has ON DELETE CASCADE — deleting endpoint should remove deliveries.
	require.NoError(t, db.DeleteWebhookEndpoint(ep.ID))

	list, err := db.ListDeliveriesByEndpoint(ep.ID, "", 100)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestWebhookDelivery_GetPending(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	now := time.Now()
	// Pending delivery with next_attempt_at in the past (should be returned).
	d1 := &model.WebhookDelivery{ID: "del-ready", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now.Add(-time.Minute), CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d1))

	// Pending delivery with next_attempt_at in the future (should not be returned).
	d2 := &model.WebhookDelivery{ID: "del-future", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now.Add(time.Hour), CreatedAt: now}
	require.NoError(t, db.CreateWebhookDelivery(d2))

	pending, err := db.GetPendingDeliveries(100)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "del-ready", pending[0].ID)
}
