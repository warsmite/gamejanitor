package testutil

import (
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/webhook"

	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/store"
	"net/http/httptest"
	"testing"

	"github.com/warsmite/gamejanitor/api"
	"github.com/warsmite/gamejanitor/config"
)

// TestAPI holds the test HTTP server and the underlying services.
type TestAPI struct {
	Server   *httptest.Server
	Services *ServiceBundle
}

// NewTestAPI creates a full HTTP test server with the chi router, all middleware, and all handler.
// The server is closed when the test finishes.
func NewTestAPI(t *testing.T) *TestAPI {
	t.Helper()

	svc := NewTestServicesWithSubscribers(t)

	cfg := config.Config{
		Bind:       "127.0.0.1",
		Port:       0,
		Controller: true,
		Worker:     true,
	}

	log := TestLogger()
	db := store.New(svc.DB)
	router := api.NewRouter(api.RouterOptions{
		Config:          cfg,
		Role:            "controller+worker",
		LogPath:         "",
		GameStore:       svc.GameStore,
		GameserverSvc:   svc.GameserverSvc,
		ConsoleSvc:      svc.ConsoleSvc,
		FileSvc:         svc.FileSvc,
		ScheduleSvc:     svc.ScheduleSvc,
		BackupSvc:       svc.BackupSvc,
		QuerySvc:        svc.QuerySvc,
		StatsPoller:     svc.StatsPoller,
		SettingsSvc:     svc.SettingsSvc,
		AuthSvc:         svc.AuthSvc,
		WorkerNodeSvc:   orchestrator.NewWorkerNodeService(db, svc.Registry, svc.Broadcaster, log),
		WebhookSvc:      webhook.NewWebhookEndpointService(db, log),
		EventHistorySvc: event.NewEventHistoryService(db),
		ActivityStore:    db,
		AccessChecker:    db.GameserverStore,
		Visibility:       db.GameserverStore,
		QuotaQuerier:     db.GameserverStore,
		Broadcaster:      svc.Broadcaster,
		ModSvc:          svc.ModSvc,
		Log:             log,
		WebUI:           nil,
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &TestAPI{
		Server:   server,
		Services: svc,
	}
}
