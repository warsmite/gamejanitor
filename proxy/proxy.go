// Package proxy provides a L4 TCP/UDP forwarder for game server traffic.
// The controller runs the proxy manager, which listens on game ports and
// forwards traffic to whichever worker node hosts each gameserver.
// This enables stable connect addresses across migrations — players always
// connect to the controller's IP, and the proxy routes to the correct backend.
package proxy

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// Route defines where traffic for a port should be forwarded.
type Route struct {
	GameserverID string
	BackendAddr  string // worker_ip:host_port
	Protocol     string // "tcp" or "udp"
}

// routeKey uniquely identifies a forwarder by port + protocol.
type routeKey struct {
	Port     int
	Protocol string
}

// Manager manages per-port/protocol forwarders. Thread-safe.
type Manager struct {
	bindAddr   string // address to bind listeners on (e.g. "0.0.0.0")
	forwarders map[routeKey]*forwarder
	mu         sync.Mutex
	log        *slog.Logger
}

// NewManager creates a proxy manager that binds listeners on the given address.
func NewManager(bindAddr string, log *slog.Logger) *Manager {
	return &Manager{
		bindAddr:   bindAddr,
		forwarders: make(map[routeKey]*forwarder),
		log:        log,
	}
}

// Set starts or updates a forwarder for the given port and protocol.
// A port can have both TCP and UDP forwarders simultaneously.
func (m *Manager) Set(port int, route Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := routeKey{Port: port, Protocol: route.Protocol}

	if f, ok := m.forwarders[key]; ok {
		f.setBackend(route.BackendAddr)
		m.log.Info("proxy route updated",
			"port", port, "backend", route.BackendAddr,
			"protocol", route.Protocol, "gameserver", route.GameserverID)
		return nil
	}

	f, err := newForwarder(m.bindAddr, port, route.Protocol, route.BackendAddr, m.log)
	if err != nil {
		return fmt.Errorf("starting forwarder on port %d/%s: %w", port, route.Protocol, err)
	}
	m.forwarders[key] = f
	m.log.Info("proxy route added",
		"port", port, "backend", route.BackendAddr,
		"protocol", route.Protocol, "gameserver", route.GameserverID)
	return nil
}

// Remove stops and removes all forwarders for the given port (both TCP and UDP).
func (m *Manager) Remove(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, proto := range []string{"tcp", "udp"} {
		key := routeKey{Port: port, Protocol: proto}
		if f, ok := m.forwarders[key]; ok {
			f.stop()
			delete(m.forwarders, key)
			m.log.Info("proxy route removed", "port", port, "protocol", proto)
		}
	}
}

// Stop shuts down all forwarders.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, f := range m.forwarders {
		f.stop()
		delete(m.forwarders, key)
	}
	m.log.Info("proxy manager stopped")
}

// Routes returns a snapshot of active routes for diagnostics.
func (m *Manager) Routes() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	routes := make(map[string]string, len(m.forwarders))
	for key, f := range m.forwarders {
		routes[fmt.Sprintf("%d/%s", key.Port, key.Protocol)] = f.getBackend()
	}
	return routes
}

// forwarder handles forwarding for a single port.
type forwarder struct {
	protocol string
	backend  string
	mu       sync.RWMutex
	done     chan struct{}
	log      *slog.Logger

	// TCP
	tcpListener net.Listener

	// UDP
	udpConn *net.UDPConn
}

func newForwarder(bindAddr string, port int, protocol string, backend string, log *slog.Logger) (*forwarder, error) {
	f := &forwarder{
		protocol: protocol,
		backend:  backend,
		done:     make(chan struct{}),
		log:      log,
	}

	listenAddr := fmt.Sprintf("%s:%d", bindAddr, port)

	switch protocol {
	case "tcp":
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return nil, err
		}
		f.tcpListener = ln
		go f.serveTCP()

	case "udp":
		addr, err := net.ResolveUDPAddr("udp", listenAddr)
		if err != nil {
			return nil, err
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			return nil, err
		}
		f.udpConn = conn
		go f.serveUDP()

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}

	return f, nil
}

func (f *forwarder) setBackend(addr string) {
	f.mu.Lock()
	f.backend = addr
	f.mu.Unlock()
}

func (f *forwarder) getBackend() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.backend
}

func (f *forwarder) stop() {
	close(f.done)
	if f.tcpListener != nil {
		f.tcpListener.Close()
	}
	if f.udpConn != nil {
		f.udpConn.Close()
	}
}
