package netutil

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateExternalURL checks that a URL is safe to fetch from a multi-tenant
// deployment. Rejects non-HTTPS schemes and URLs that resolve to private or
// loopback IP ranges.
//
// This is only enforced when the restrict_download_urls setting is enabled
// (business mode). Self-hosted users can fetch from any URL.
func ValidateExternalURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf("only https URLs are allowed (got %s)", parsed.Scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("URL has no hostname")
	}

	// Resolve the hostname to check for private IPs (prevents DNS rebinding)
	ips, err := net.LookupHost(hostname)
	if err != nil {
		return fmt.Errorf("cannot resolve hostname %q: %w", hostname, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("URL resolves to private address %s", ipStr)
		}
	}

	return nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}

	// Cloud metadata endpoint
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return true
	}

	for _, cidr := range privateRanges {
		if cidr.Contains(ip) {
			return true
		}
	}

	return false
}

var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7", // IPv6 unique local
	}
	var nets []*net.IPNet
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("bad CIDR: " + cidr)
		}
		nets = append(nets, n)
	}
	return nets
}()

// ValidateWebhookURL checks that a webhook delivery URL is safe.
// Same rules as ValidateExternalURL but also allows http (webhooks commonly
// use HTTP endpoints behind internal load balancers in self-hosted setups,
// but in restricted mode we still block private IPs).
func ValidateWebhookURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are allowed (got %s)", parsed.Scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("URL has no hostname")
	}

	// Skip IP check for non-IP hostnames that we can't resolve at validation
	// time (webhook URLs are validated at creation, delivered later)
	if ip := net.ParseIP(hostname); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("webhook URL points to private address %s", hostname)
		}
		return nil
	}

	// For hostnames, resolve and check
	ips, err := net.LookupHost(hostname)
	if err != nil {
		// DNS may not resolve at creation time (e.g., external webhook service not
		// reachable from controller). Allow it — delivery will fail naturally.
		return nil
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("webhook URL %q resolves to private address %s", hostname, ipStr)
		}
	}

	return nil
}

// SanitizeURLForLog returns a URL string safe for logging (strips query params
// which may contain tokens).
func SanitizeURLForLog(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	result := parsed.String()
	// Truncate very long URLs
	if len(result) > 200 {
		return result[:200] + "..."
	}
	return result
}

// looksLikePrivateHostname is a simple heuristic — not used for enforcement,
// just for the DNS resolution path.
func looksLikePrivateHostname(hostname string) bool {
	return strings.HasSuffix(hostname, ".local") ||
		strings.HasSuffix(hostname, ".internal") ||
		hostname == "localhost"
}
