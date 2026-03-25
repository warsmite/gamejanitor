package netinfo

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Info holds the detected network addresses for this host.
type Info struct {
	ExternalIP string
	LANIP      string
}

// Detect discovers the host's LAN and external IP addresses.
func Detect(log *slog.Logger) *Info {
	info := &Info{}

	info.LANIP = detectLANIP(log)
	info.ExternalIP = detectExternalIP(log)

	log.Info("detected network info",
		"lan_ip", info.LANIP,
		"external_ip", info.ExternalIP,
	)

	return info
}

// virtualPrefixes covers common VPN, container, and virtual interface names.
var virtualPrefixes = []string{
	"tun", "tap",      // OpenVPN, generic tunnels
	"wg",              // WireGuard
	"mullvad",         // Mullvad VPN
	"tailscale", "ts", // Tailscale
	"docker", "br-",   // Docker
	"veth",            // Docker/container veth pairs
	"virbr",           // libvirt
	"lo",              // loopback
}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func detectLANIP(log *slog.Logger) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Warn("failed to list network interfaces", "error", err)
		return ""
	}

	var fallback string

	for _, iface := range ifaces {
		// Skip down, loopback, and virtual interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			ip := ipNet.IP

			if !ip.IsPrivate() {
				continue
			}

			log.Debug("found LAN candidate", "interface", iface.Name, "ip", ip.String())

			// Prefer 192.168.x.x and 172.16-31.x.x (with /24 or smaller subnets)
			// over 10.x.x.x, since 10.x.x.x is commonly used by VPNs.
			// Docker/Podman use large subnets (/16, /20) in the 172.x range,
			// so we filter those out by checking the subnet mask.
			if ip[0] == 192 {
				return ip.String()
			}
			if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
				ones, _ := ipNet.Mask.Size()
				if ones >= 24 {
					return ip.String()
				}
				log.Debug("skipping large subnet in 172.x range (likely container network)", "interface", iface.Name, "ip", ip.String(), "mask", ones)
			}
			if fallback == "" {
				fallback = ip.String()
			}
		}
	}

	if fallback != "" {
		return fallback
	}

	log.Warn("no suitable LAN IP found")
	return ""
}

func detectExternalIP(log *slog.Logger) string {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("https://icanhazip.com")
	if err != nil {
		log.Warn("failed to detect external IP", "error", err)
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		log.Warn("failed to read external IP response", "error", err)
		return ""
	}

	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		log.Warn("external IP service returned invalid IP", "response", ip)
		return ""
	}

	return ip
}
