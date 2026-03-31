package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTCPProxy(t *testing.T) {
	// Start a TCP echo backend
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backendLn.Close()
	go func() {
		for {
			conn, err := backendLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	mgr := NewManager("127.0.0.1", testLogger())
	defer mgr.Stop()

	port := freePort(t)
	err = mgr.Set(port, Route{
		GameserverID: "gs-1",
		BackendAddr:  backendLn.Addr().String(),
		Protocol:     "tcp",
	})
	require.NoError(t, err)

	// Connect through the proxy
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	buf := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}

func TestUDPProxy(t *testing.T) {
	// Start a UDP echo backend
	backendAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)
	backendConn, err := net.ListenUDP("udp", backendAddr)
	require.NoError(t, err)
	defer backendConn.Close()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := backendConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			backendConn.WriteToUDP(buf[:n], addr)
		}
	}()

	mgr := NewManager("127.0.0.1", testLogger())
	defer mgr.Stop()

	port := freePort(t)
	err = mgr.Set(port, Route{
		GameserverID: "gs-1",
		BackendAddr:  backendConn.LocalAddr().String(),
		Protocol:     "udp",
	})
	require.NoError(t, err)

	// Send through the proxy
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	buf := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}

func TestBackendUpdate(t *testing.T) {
	// Backend 1
	backend1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend1.Close()
	go func() {
		for {
			conn, err := backend1.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("backend1"))
			conn.Close()
		}
	}()

	// Backend 2
	backend2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend2.Close()
	go func() {
		for {
			conn, err := backend2.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("backend2"))
			conn.Close()
		}
	}()

	mgr := NewManager("127.0.0.1", testLogger())
	defer mgr.Stop()

	port := freePort(t)
	mgr.Set(port, Route{GameserverID: "gs-1", BackendAddr: backend1.Addr().String(), Protocol: "tcp"})

	// Read from backend 1
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	buf := make([]byte, 8)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := conn.Read(buf)
	conn.Close()
	assert.Equal(t, "backend1", string(buf[:n]))

	// Update route to backend 2 (simulates migration)
	mgr.Set(port, Route{GameserverID: "gs-1", BackendAddr: backend2.Addr().String(), Protocol: "tcp"})

	// Read from backend 2
	conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ = conn.Read(buf)
	conn.Close()
	assert.Equal(t, "backend2", string(buf[:n]))
}

func TestRemoveStopsListening(t *testing.T) {
	mgr := NewManager("127.0.0.1", testLogger())
	defer mgr.Stop()

	port := freePort(t)
	mgr.Set(port, Route{GameserverID: "gs-1", BackendAddr: "127.0.0.1:9999", Protocol: "tcp"})

	// Port should be in use
	_, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.Error(t, err, "port should be in use")

	mgr.Remove(port)

	// Port should be free
	time.Sleep(50 * time.Millisecond)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "port should be free after remove")
	if ln != nil {
		ln.Close()
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
