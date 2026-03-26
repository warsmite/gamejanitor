package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestWebhookEndpoint_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{
		Description: "Test Endpoint",
		URL:         "https://example.com/webhook",
		Secret:      "s3cret",
		Events:      `["*"]`,
		Enabled:     true,
	}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))
	assert.NotEmpty(t, ep.ID, "ID should be auto-generated")
	assert.False(t, ep.CreatedAt.IsZero())

	got, err := model.GetWebhookEndpoint(db, ep.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, ep.ID, got.ID)
	assert.Equal(t, "Test Endpoint", got.Description)
	assert.Equal(t, "https://example.com/webhook", got.URL)
	assert.Equal(t, "s3cret", got.Secret)
	assert.Equal(t, `["*"]`, got.Events)
	assert.True(t, got.Enabled)
}

func TestWebhookEndpoint_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	got, err := model.GetWebhookEndpoint(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWebhookEndpoint_Update(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{
		Description: "Original",
		URL:         "https://example.com/original",
		Events:      `["*"]`,
		Enabled:     true,
	}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	ep.Description = "Updated"
	ep.URL = "https://example.com/updated"
	ep.Enabled = false
	require.NoError(t, model.UpdateWebhookEndpoint(db, ep))

	got, err := model.GetWebhookEndpoint(db, ep.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Updated", got.Description)
	assert.Equal(t, "https://example.com/updated", got.URL)
	assert.False(t, got.Enabled)
}

func TestWebhookEndpoint_UpdateNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{ID: "nonexistent", URL: "https://example.com"}
	err := model.UpdateWebhookEndpoint(db, ep)
	require.Error(t, err)
}

func TestWebhookEndpoint_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{
		URL:     "https://example.com/delete",
		Events:  `["*"]`,
		Enabled: true,
	}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	require.NoError(t, model.DeleteWebhookEndpoint(db, ep.ID))

	got, err := model.GetWebhookEndpoint(db, ep.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWebhookEndpoint_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	err := model.DeleteWebhookEndpoint(db, "nonexistent")
	require.Error(t, err)
}

func TestWebhookEndpoint_List(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep1 := &model.WebhookEndpoint{URL: "https://a.com", Events: `["*"]`, Enabled: true}
	ep2 := &model.WebhookEndpoint{URL: "https://b.com", Events: `["*"]`, Enabled: false}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep1))
	require.NoError(t, model.CreateWebhookEndpoint(db, ep2))

	list, err := model.ListWebhookEndpoints(db)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestWebhookEndpoint_ListEnabled(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep1 := &model.WebhookEndpoint{URL: "https://a.com", Events: `["*"]`, Enabled: true}
	ep2 := &model.WebhookEndpoint{URL: "https://b.com", Events: `["*"]`, Enabled: false}
	ep3 := &model.WebhookEndpoint{URL: "https://c.com", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep1))
	require.NoError(t, model.CreateWebhookEndpoint(db, ep2))
	require.NoError(t, model.CreateWebhookEndpoint(db, ep3))

	list, err := model.ListEnabledWebhookEndpoints(db)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	for _, ep := range list {
		assert.True(t, ep.Enabled)
	}
}

// --- Webhook Delivery Tests ---

func TestWebhookDelivery_CreateAndListByEndpoint(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d := &model.WebhookDelivery{
		ID:                "del-1",
		WebhookEndpointID: ep.ID,
		EventType:         "gameserver.started",
		Payload:           `{"id":"gs-1"}`,
		NextAttemptAt:     now,
		CreatedAt:         now,
	}
	require.NoError(t, model.CreateWebhookDelivery(db, d))

	list, err := model.ListDeliveriesByEndpoint(db, ep.ID, "", 100)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "del-1", list[0].ID)
	assert.Equal(t, model.WebhookStatePending, list[0].State)
	assert.Equal(t, 0, list[0].Attempts)
}

func TestWebhookDelivery_ListByEndpoint_FilterByState(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d1 := &model.WebhookDelivery{ID: "del-p", WebhookEndpointID: ep.ID, EventType: "a", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d1))

	d2 := &model.WebhookDelivery{ID: "del-d", WebhookEndpointID: ep.ID, EventType: "b", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d2))
	require.NoError(t, model.MarkDeliverySuccess(db, "del-d"))

	pending, err := model.ListDeliveriesByEndpoint(db, ep.ID, model.WebhookStatePending, 100)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "del-p", pending[0].ID)

	delivered, err := model.ListDeliveriesByEndpoint(db, ep.ID, model.WebhookStateDelivered, 100)
	require.NoError(t, err)
	assert.Len(t, delivered, 1)
	assert.Equal(t, "del-d", delivered[0].ID)
}

func TestWebhookDelivery_MarkSuccess(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-suc", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d))

	require.NoError(t, model.MarkDeliverySuccess(db, "del-suc"))

	list, err := model.ListDeliveriesByEndpoint(db, ep.ID, model.WebhookStateDelivered, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, model.WebhookStateDelivered, list[0].State)
	assert.Equal(t, 1, list[0].Attempts)
}

func TestWebhookDelivery_MarkRetry(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-retry", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d))

	nextAttempt := now.Add(5 * time.Minute)
	require.NoError(t, model.MarkDeliveryRetry(db, "del-retry", nextAttempt, "connection refused"))

	list, err := model.ListDeliveriesByEndpoint(db, ep.ID, model.WebhookStatePending, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, 1, list[0].Attempts)
	assert.Equal(t, "connection refused", list[0].LastError)
}

func TestWebhookDelivery_MarkFailed(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-fail", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d))

	require.NoError(t, model.MarkDeliveryFailed(db, "del-fail", "max retries exceeded"))

	list, err := model.ListDeliveriesByEndpoint(db, ep.ID, model.WebhookStateFailed, 100)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, model.WebhookStateFailed, list[0].State)
	assert.Equal(t, "max retries exceeded", list[0].LastError)
}

func TestWebhookDelivery_CascadeOnEndpointDelete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	d := &model.WebhookDelivery{ID: "del-cas", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now, CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d))

	// webhook_deliveries has ON DELETE CASCADE — deleting endpoint should remove deliveries.
	require.NoError(t, model.DeleteWebhookEndpoint(db, ep.ID))

	list, err := model.ListDeliveriesByEndpoint(db, ep.ID, "", 100)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestWebhookDelivery_GetPending(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ep := &model.WebhookEndpoint{URL: "https://example.com/hook", Events: `["*"]`, Enabled: true}
	require.NoError(t, model.CreateWebhookEndpoint(db, ep))

	now := time.Now()
	// Pending delivery with next_attempt_at in the past (should be returned).
	d1 := &model.WebhookDelivery{ID: "del-ready", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now.Add(-time.Minute), CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d1))

	// Pending delivery with next_attempt_at in the future (should not be returned).
	d2 := &model.WebhookDelivery{ID: "del-future", WebhookEndpointID: ep.ID, EventType: "test", Payload: `{}`, NextAttemptAt: now.Add(time.Hour), CreatedAt: now}
	require.NoError(t, model.CreateWebhookDelivery(db, d2))

	pending, err := model.GetPendingDeliveries(db, 100)
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "del-ready", pending[0].ID)
}
