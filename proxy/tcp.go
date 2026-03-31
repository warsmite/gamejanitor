package proxy

import (
	"io"
	"net"
)

func (f *forwarder) serveTCP() {
	for {
		conn, err := f.tcpListener.Accept()
		if err != nil {
			select {
			case <-f.done:
				return
			default:
				f.log.Error("proxy tcp accept error", "error", err)
				continue
			}
		}
		go f.handleTCP(conn)
	}
}

func (f *forwarder) handleTCP(client net.Conn) {
	defer client.Close()

	backend := f.getBackend()
	if backend == "" {
		return
	}

	server, err := net.Dial("tcp", backend)
	if err != nil {
		f.log.Debug("proxy tcp dial failed", "backend", backend, "error", err)
		return
	}
	defer server.Close()

	done := make(chan struct{})
	go func() {
		io.Copy(server, client)
		close(done)
	}()
	io.Copy(client, server)
	<-done
}
