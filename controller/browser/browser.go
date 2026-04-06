// Package browser integrates with the gamejanitor browser service (gamejanitor.net)
// to check public reachability of gameservers and optionally register them.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
)

const (
	defaultBaseURL = "https://gamejanitor.net"
	checkTimeout   = 15 * time.Second
)

// CheckResult is the response from the browser check endpoint.
type CheckResult struct {
	Reachable  bool   `json:"reachable"`
	Name       string `json:"name,omitempty"`
	Players    int    `json:"players,omitempty"`
	MaxPlayers int    `json:"max_players,omitempty"`
	Map        string `json:"map,omitempty"`
	Version    string `json:"version,omitempty"`
	GamePort   int    `json:"game_port,omitempty"`
	QueryPort  int    `json:"query_port,omitempty"`
	Registered bool   `json:"registered,omitempty"`
}

// ReachabilityStore is the minimal store interface needed by the checker.
type ReachabilityStore interface {
	GetGameserver(id string) (*model.Gameserver, error)
	GetWorkerNode(id string) (*model.WorkerNode, error)
}

// ReachabilityChecker probes gameserver reachability via the browser service
// when a gameserver reaches "running" status. Results are cached in memory
// and emitted as events.
type ReachabilityChecker struct {
	bus         *controller.EventBus
	settingsSvc *settings.SettingsService
	store       ReachabilityStore
	log         *slog.Logger
	client      *http.Client
	baseURL     string
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// Cached results per gameserver
	results map[string]*CheckResult
	mu      sync.RWMutex
}

func New(bus *controller.EventBus, settingsSvc *settings.SettingsService, store ReachabilityStore, log *slog.Logger) *ReachabilityChecker {
	return &ReachabilityChecker{
		bus:         bus,
		settingsSvc: settingsSvc,
		store:       store,
		log:         log,
		client:      &http.Client{Timeout: checkTimeout},
		baseURL:     defaultBaseURL,
		results:     make(map[string]*CheckResult),
	}
}

// Start subscribes to the event bus and watches for gameserver.ready events.
func (c *ReachabilityChecker) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)

	ch, unsub := c.bus.Subscribe()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				if e, ok := event.(controller.Event); ok && e.Type == controller.EventGameserverReady {
					go c.check(ctx, e.GameserverID)
				}
			}
		}
	}()

	c.log.Info("reachability checker started")
}

func (c *ReachabilityChecker) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.log.Info("reachability checker stopped")
}

// GetResult returns the cached reachability result for a gameserver, or nil.
func (c *ReachabilityChecker) GetResult(gameserverID string) *CheckResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.results[gameserverID]
}

func (c *ReachabilityChecker) check(ctx context.Context, gameserverID string) {
	gs, err := c.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		return
	}

	host, port := c.resolveAddress(gs)
	if host == "" || port == 0 {
		c.log.Debug("cannot check reachability, no host/port resolved", "id", gameserverID)
		return
	}

	gameID := gs.GameID
	register := c.settingsSvc.GetBool(settings.SettingRegisterWithBrowser)

	params := url.Values{
		"host": {host},
		"port": {fmt.Sprintf("%d", port)},
		"game": {gameID},
	}
	if register {
		params.Set("register", "true")
	}

	reqURL := fmt.Sprintf("%s/api/v1/check?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		c.log.Debug("failed to build reachability request", "id", gameserverID, "error", err)
		return
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// Fault tolerant — silently skip on failure
		c.log.Debug("reachability check failed, skipping", "id", gameserverID, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Debug("reachability check returned non-200", "id", gameserverID, "status", resp.StatusCode)
		return
	}

	var result CheckResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.log.Debug("failed to decode reachability response", "id", gameserverID, "error", err)
		return
	}

	// Cache result
	c.mu.Lock()
	c.results[gameserverID] = &result
	c.mu.Unlock()

	c.log.Info("reachability check complete",
		"id", gameserverID, "reachable", result.Reachable, "host", host, "port", port, "registered", result.Registered)

	c.bus.Publish(controller.NewSystemEvent(controller.EventGameserverReachable, gameserverID, &controller.ReachableData{
		Reachable:  result.Reachable,
		Host:       host,
		Port:       port,
		Registered: result.Registered,
	}))
}

// resolveAddress determines the public host and game port for a gameserver.
// Priority: gameserver connection_address > global connection_address > worker external_ip > worker lan_ip
func (c *ReachabilityChecker) resolveAddress(gs *model.Gameserver) (string, int) {
	// Get first game port
	port := 0
	for _, p := range gs.Ports {
		if p.Name == "game" {
			port = int(p.HostPort)
			break
		}
	}
	if port == 0 && len(gs.Ports) > 0 {
		port = int(gs.Ports[0].HostPort)
	}

	// Resolve host — gameserver override first
	if gs.ConnectionAddress != nil && *gs.ConnectionAddress != "" {
		return *gs.ConnectionAddress, port
	}

	// Fall back to settings/worker IP resolution
	host, _ := c.settingsSvc.ResolveConnectionIP(gs.NodeID)
	return host, port
}
