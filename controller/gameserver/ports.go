package gameserver

import (
	"fmt"
	"net"
)

func ptrIntOr0(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

// isPortAvailable checks if a port is free on the host by attempting to bind it.
func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
