package handler_test

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/testutil"
)

// readSSEEvents connects to the SSE endpoint, publishes events via publishFn,
// then reads from the stream with a timeout. Returns the collected "data:" lines.
func readSSEEvents(t *testing.T, url string, token string, publishFn func(), timeout time.Duration) []string {
	t.Helper()

	req := authRequest("GET", url, token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	// Small delay to let the SSE handler subscribe to the event bus
	time.Sleep(50 * time.Millisecond)
	publishFn()

	scanner := bufio.NewScanner(resp.Body)
	var received []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				received = append(received, line)
			}
		}
	}()

	time.Sleep(timeout)
	resp.Body.Close()
	<-done

	return received
}

func TestSSE_ScopedToken_OnlyReceivesOwnEvents(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)

	gsA := createGameserverWithToken(t, api, adminToken, "Server A")
	gsB := createGameserverWithToken(t, api, adminToken, "Server B")

	scopedToken := testutil.MustCreateUserToken(t, api.Services,
		auth.AllPermissions, []string{gsA})

	received := readSSEEvents(t, api.Server.URL+"/api/events", scopedToken, func() {
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverReady, gsA, nil))
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverReady, gsB, nil))
	}, 300*time.Millisecond)

	var hasA, hasB bool
	for _, line := range received {
		if strings.Contains(line, gsA) {
			hasA = true
		}
		if strings.Contains(line, gsB) {
			hasB = true
		}
	}
	assert.True(t, hasA, "scoped token should receive events for gameserver A")
	assert.False(t, hasB, "scoped token should NOT receive events for gameserver B")
}

func TestSSE_AdminToken_ReceivesAllEvents(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)

	gsA := createGameserverWithToken(t, api, adminToken, "Server A")
	gsB := createGameserverWithToken(t, api, adminToken, "Server B")

	received := readSSEEvents(t, api.Server.URL+"/api/events", adminToken, func() {
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverReady, gsA, nil))
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverReady, gsB, nil))
	}, 300*time.Millisecond)

	var hasA, hasB bool
	for _, line := range received {
		if strings.Contains(line, gsA) {
			hasA = true
		}
		if strings.Contains(line, gsB) {
			hasB = true
		}
	}
	assert.True(t, hasA, "admin token should receive events for gameserver A")
	assert.True(t, hasB, "admin token should receive events for gameserver B")
}

func TestSSE_TypeFilter(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsA := createGameserverWithToken(t, api, adminToken, "Server A")

	received := readSSEEvents(t, api.Server.URL+"/api/events?types=gameserver.ready", adminToken, func() {
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverReady, gsA, nil))
		api.Services.Broadcaster.Publish(controller.NewSystemEvent(controller.EventGameserverStats, gsA, &controller.StatsData{
			CPUPercent:    5.0,
			MemoryUsageMB: 128,
			MemoryLimitMB: 512,
		}))
	}, 300*time.Millisecond)

	var hasReady, hasStats bool
	for _, line := range received {
		if strings.Contains(line, gsA) && !strings.Contains(line, "cpu_percent") {
			hasReady = true
		}
		if strings.Contains(line, "cpu_percent") {
			hasStats = true
		}
	}
	assert.True(t, hasReady, "gameserver.ready event should pass the type filter")
	assert.False(t, hasStats, "gameserver.stats event should be filtered out")
}

