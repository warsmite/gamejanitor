package netutil

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"::1", true},

		// RFC 1918
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},

		// Link-local
		{"169.254.1.1", true},
		{"169.254.169.254", true}, // cloud metadata

		// Unspecified
		{"0.0.0.0", true},

		// Public IPs
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},

		// Non-private 172.x
		{"172.15.0.1", false},
		{"172.32.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %s", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https allowed", "https://cdn.modrinth.com/mod.jar", false},
		{"http blocked", "http://cdn.modrinth.com/mod.jar", true},
		{"ftp blocked", "ftp://files.example.com/mod.jar", true},
		{"file blocked", "file:///etc/passwd", true},
		{"no scheme", "cdn.modrinth.com/mod.jar", true},
		{"empty", "", true},
		{"localhost blocked", "https://localhost/mod.jar", true},
		{"loopback blocked", "https://127.0.0.1/mod.jar", true},
		{"private 10.x blocked", "https://10.0.0.1/mod.jar", true},
		{"private 172.16.x blocked", "https://172.16.0.1/mod.jar", true},
		{"private 192.168.x blocked", "https://192.168.1.1/mod.jar", true},
		{"metadata IP blocked", "https://169.254.169.254/latest", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExternalURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https allowed", "https://hooks.slack.com/services/foo", false},
		{"http allowed", "http://hooks.example.com/webhook", false},
		{"ftp blocked", "ftp://files.example.com/hook", true},
		{"file blocked", "file:///etc/passwd", true},
		{"loopback IP blocked", "http://127.0.0.1:8080/hook", true},
		{"private 10.x IP blocked", "http://10.0.0.1/hook", true},
		{"private 192.168.x IP blocked", "https://192.168.1.100:3000/hook", true},
		{"metadata IP blocked", "http://169.254.169.254/latest/meta-data", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWebhookURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeURLForLog(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/mod.jar?token=secret", "https://example.com/mod.jar"},
		{"https://example.com/path#fragment", "https://example.com/path"},
		{"not a url % invalid", "<invalid-url>"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeURLForLog(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeURLForLog(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
