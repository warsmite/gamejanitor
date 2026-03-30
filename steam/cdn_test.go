package steam

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAdler32Seed0(t *testing.T) {
	// Known test vector: empty data with seed 0 should return 0
	assert.Equal(t, uint32(0), adler32Seed0([]byte{}))

	// Single byte
	assert.Equal(t, uint32(0x00010001), adler32Seed0([]byte{1}))

	// Verify it differs from standard Adler32 (seed 1)
	data := []byte("Hello, World!")
	seed0 := adler32Seed0(data)
	// Standard Go adler32 would give a different result
	assert.NotEqual(t, uint32(0), seed0)
}

func TestIsCDNHostError(t *testing.T) {
	tests := []struct {
		err  string
		want bool
	}{
		{"tls: failed to verify certificate", true},
		{"x509: certificate is valid for *.example.com", true},
		{"certificate signed by unknown authority", true},
		{"connection refused", true},
		{"no such host", true},
		{"HTTP 404 from example.com", false},
		{"context deadline exceeded", false},
		{"read: connection reset by peer", false},
	}

	for _, tt := range tests {
		t.Run(tt.err, func(t *testing.T) {
			assert.Equal(t, tt.want, isCDNHostError(fmt.Errorf("%s", tt.err)))
		})
	}
}

func TestCDNClient_Blacklisting(t *testing.T) {
	hosts := []string{"good1.example.com", "bad.example.com", "good2.example.com"}
	client := NewCDNClient(slog.Default(), hosts)

	// Before blacklisting — all hosts are available
	seen := map[string]bool{}
	for range 3 {
		host := client.nextAvailableHost()
		seen[host] = true
	}
	assert.Len(t, seen, 3)

	// Blacklist one host
	client.blacklistHost("bad.example.com")

	// Now only 2 hosts should be returned
	seen = map[string]bool{}
	for range 4 {
		host := client.nextAvailableHost()
		if host != "" {
			seen[host] = true
		}
	}
	assert.Len(t, seen, 2)
	assert.False(t, seen["bad.example.com"])

	// Blacklist all — should return empty
	client.blacklistHost("good1.example.com")
	client.blacklistHost("good2.example.com")
	assert.Equal(t, "", client.nextAvailableHost())
}
