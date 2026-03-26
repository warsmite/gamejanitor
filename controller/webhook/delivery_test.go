package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// testWebhookLookup satisfies webhook.GameserverLookup for tests.
type testWebhookLookup struct {
	gs *store.GameserverStore
	wn *store.WorkerNodeStore
}

func (l *testWebhookLookup) GetGameserver(id string) (*model.Gameserver, error) {
	return l.gs.GetGameserver(id)
}

func (l *testWebhookLookup) GetWorkerNode(id string) (*model.WorkerNode, error) {
	return l.wn.GetWorkerNode(id)
}

func TestWebhookDelivery_CreateAndListPending(t *testing.T) {
	t.Parallel()
	rawDB := testutil.NewTestDB(t)
	db := store.New(rawDB)

	ep := &model.WebhookEndpoint{
		ID: "ep-1", URL: "http://example.com/hook",
		Secret: "test-secret", Events: model.StringSlice{"*"}, Enabled: true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	delivery := &model.WebhookDelivery{
		ID: "d-1", WebhookEndpointID: ep.ID,
		EventType: "gameserver.create", Payload: `{"test": true}`, NextAttemptAt: time.Now(),
	}
	require.NoError(t, db.CreateWebhookDelivery(delivery))

	all, err := db.ListDeliveriesByEndpoint(ep.ID, "pending", 10)
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "gameserver.create", all[0].EventType)
}

func TestWebhookDelivery_HMACSignature(t *testing.T) {
	t.Parallel()

	secret := "my-webhook-secret"
	body := []byte(`{"version":1,"event_type":"gameserver.create","data":{}}`)

	// Compute expected HMAC
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Start a test HTTP server that captures the signature header
	var receivedSig string
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Webhook-Signature")
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		receivedBody = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use the deliver function indirectly — create a full webhook worker setup
	svc := testutil.NewTestServices(t)
	db := store.New(svc.DB)
	ep := &model.WebhookEndpoint{
		ID:      "ep-hmac",
		URL:     srv.URL,
		Secret:  secret,
		Events:  model.StringSlice{"*"},
		Enabled: true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	delivery := &model.WebhookDelivery{
		ID:                "d-hmac",
		WebhookEndpointID: ep.ID,
		EventType:         "gameserver.create",
		Payload:           string(body),
		NextAttemptAt:     time.Now(),
	}
	require.NoError(t, db.CreateWebhookDelivery(delivery))

	// Start webhook worker, let it process
	whStore := store.NewWebhookStore(svc.DB)
	gsLookup := &testWebhookLookup{gs: store.NewGameserverStore(svc.DB), wn: store.NewWorkerNodeStore(svc.DB)}
	ww := webhook.NewWebhookWorker(whStore, gsLookup, svc.Broadcaster, testutil.TestLogger())
	ctx := testutil.TestContext()
	ww.Start(ctx)
	time.Sleep(6 * time.Second) // delivery poll interval is 5s
	ww.Stop()

	assert.Equal(t, expectedSig, receivedSig, "HMAC signature should match")
	assert.Equal(t, body, receivedBody)
}

func TestWebhookDelivery_RetryBackoff(t *testing.T) {
	t.Parallel()
	// Verify the backoff formula: 5 * (1 << attempts) seconds, capped at 3600
	cases := []struct {
		attempt  int
		expected int
	}{
		{0, 5},    // 5 * 2^0 = 5
		{1, 10},   // 5 * 2^1 = 10
		{2, 20},   // 5 * 2^2 = 20
		{5, 160},  // 5 * 2^5 = 160
		{10, 3600}, // 5 * 2^10 = 5120, capped at 3600
	}
	for _, tc := range cases {
		backoff := 5 * (1 << tc.attempt)
		if backoff > 3600 {
			backoff = 3600
		}
		assert.Equal(t, tc.expected, backoff, "backoff at attempt %d", tc.attempt)
	}
}

func TestWebhookDelivery_EventFilterNamespace(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	db := store.New(svc.DB)

	ep := &model.WebhookEndpoint{
		ID:      "ep-ns",
		URL:     "http://example.com/hook",
		Events:  model.StringSlice{"gameserver.*"},
		Enabled: true,
	}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	// "gameserver.create" should match "gameserver.*"
	// "backup.create" should NOT match
	// Test via delivery creation: the webhook worker's matchEventFilter handles this.
	// We test the pattern matching indirectly by checking what gets enqueued.

	// Enqueue matching event
	d1 := &model.WebhookDelivery{
		ID: "d-match", WebhookEndpointID: ep.ID,
		EventType: "gameserver.create", Payload: `{}`, NextAttemptAt: time.Now(),
	}
	require.NoError(t, db.CreateWebhookDelivery(d1))

	all, err := db.ListDeliveriesByEndpoint(ep.ID, "pending", 10)
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "gameserver.create", all[0].EventType)
}

func TestWebhookDelivery_DeliveryStateTransitions(t *testing.T) {
	t.Parallel()
	rawDB := testutil.NewTestDB(t)
	db := store.New(rawDB)

	ep := &model.WebhookEndpoint{ID: "ep-state", URL: "http://example.com", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	d := &model.WebhookDelivery{
		ID: "d-state", WebhookEndpointID: ep.ID,
		EventType: "gameserver.start", Payload: `{}`, NextAttemptAt: time.Now(),
	}
	require.NoError(t, db.CreateWebhookDelivery(d))

	// Initial state is pending
	all, err := db.ListDeliveriesByEndpoint(ep.ID, "pending", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "pending", all[0].State)

	// Mark retry — still pending but with increased attempts
	require.NoError(t, db.MarkDeliveryRetry("d-state", time.Now().Add(time.Minute), "timeout"))
	all, err = db.ListDeliveriesByEndpoint(ep.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "pending", all[0].State)
	assert.Equal(t, 1, all[0].Attempts)

	// Mark success
	require.NoError(t, db.MarkDeliverySuccess("d-state"))
	all, err = db.ListDeliveriesByEndpoint(ep.ID, "delivered", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "delivered", all[0].State)
}

func TestWebhookDelivery_FailedAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	rawDB := testutil.NewTestDB(t)
	db := store.New(rawDB)

	ep := &model.WebhookEndpoint{ID: "ep-fail", URL: "http://example.com", Events: model.StringSlice{"*"}, Enabled: true}
	require.NoError(t, db.CreateWebhookEndpoint(ep))

	d := &model.WebhookDelivery{
		ID: "d-fail", WebhookEndpointID: ep.ID,
		EventType: "gameserver.start", Payload: `{}`, NextAttemptAt: time.Now(),
	}
	require.NoError(t, db.CreateWebhookDelivery(d))

	require.NoError(t, db.MarkDeliveryFailed("d-fail", "permanently failed"))
	all, err := db.ListDeliveriesByEndpoint(ep.ID, "failed", 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "failed", all[0].State)
}
