package proxy

import (
	"net"
	"sync"
	"time"
)

const (
	udpBufSize       = 65535
	udpSessionTTL    = 30 * time.Second
	udpCleanupPeriod = 10 * time.Second
)

// udpSession tracks a client's upstream connection for return traffic.
type udpSession struct {
	backendConn *net.UDPConn
	clientAddr  *net.UDPAddr
	lastActive  time.Time
}

func (f *forwarder) serveUDP() {
	sessions := make(map[string]*udpSession)
	var sessionsMu sync.Mutex

	// Cleanup stale sessions
	cleanupTicker := time.NewTicker(udpCleanupPeriod)
	go func() {
		defer cleanupTicker.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-cleanupTicker.C:
				sessionsMu.Lock()
				now := time.Now()
				for key, s := range sessions {
					if now.Sub(s.lastActive) > udpSessionTTL {
						s.backendConn.Close()
						delete(sessions, key)
					}
				}
				sessionsMu.Unlock()
			}
		}
	}()

	buf := make([]byte, udpBufSize)
	for {
		n, clientAddr, err := f.udpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-f.done:
				// Clean up all sessions
				sessionsMu.Lock()
				for _, s := range sessions {
					s.backendConn.Close()
				}
				sessionsMu.Unlock()
				return
			default:
				f.log.Error("proxy udp read error", "error", err)
				continue
			}
		}

		backend := f.getBackend()
		if backend == "" {
			continue
		}

		key := clientAddr.String()

		sessionsMu.Lock()
		sess, ok := sessions[key]
		if !ok {
			// New client — create upstream connection
			backendAddr, err := net.ResolveUDPAddr("udp", backend)
			if err != nil {
				sessionsMu.Unlock()
				f.log.Debug("proxy udp resolve failed", "backend", backend, "error", err)
				continue
			}
			backendConn, err := net.DialUDP("udp", nil, backendAddr)
			if err != nil {
				sessionsMu.Unlock()
				f.log.Debug("proxy udp dial failed", "backend", backend, "error", err)
				continue
			}
			sess = &udpSession{
				backendConn: backendConn,
				clientAddr:  clientAddr,
				lastActive:  time.Now(),
			}
			sessions[key] = sess

			// Return path: backend → client
			go f.udpReturnPath(sess, f.udpConn)
		}
		sess.lastActive = time.Now()
		sessionsMu.Unlock()

		// Forward client → backend
		sess.backendConn.Write(buf[:n])
	}
}

// udpReturnPath reads from the backend and writes back to the client.
func (f *forwarder) udpReturnPath(sess *udpSession, frontConn *net.UDPConn) {
	buf := make([]byte, udpBufSize)
	for {
		sess.backendConn.SetReadDeadline(time.Now().Add(udpSessionTTL))
		n, err := sess.backendConn.Read(buf)
		if err != nil {
			return
		}
		frontConn.WriteToUDP(buf[:n], sess.clientAddr)
	}
}
