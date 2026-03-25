package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install [--systemd|--launchd]",
	Short: "Install as a system service",
	Long:  "Installs gamejanitor as a system service so it starts on boot and survives terminal closure.",
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().Bool("systemd", false, "Force systemd service installation")
	installCmd.Flags().Bool("launchd", false, "Force launchd service installation (macOS)")
}

func runInstall(cmd *cobra.Command, args []string) error {
	forceSystemd, _ := cmd.Flags().GetBool("systemd")
	forceLaunchd, _ := cmd.Flags().GetBool("launchd")

	if forceSystemd && forceLaunchd {
		return exitError(fmt.Errorf("cannot specify both --systemd and --launchd"))
	}

	if forceSystemd {
		return installSystemd()
	}
	if forceLaunchd {
		return installLaunchd()
	}

	// Auto-detect
	if runtime.GOOS == "darwin" {
		return installLaunchd()
	}

	if _, err := exec.LookPath("systemctl"); err == nil {
		return installSystemd()
	}

	return exitError(fmt.Errorf("could not detect init system\n  Use --systemd or --launchd to specify manually"))
}

const systemdUnit = `[Unit]
Description=Gamejanitor - Game Server Manager
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=5

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/gamejanitor

[Install]
WantedBy=multi-user.target
`

func installSystemd() error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	unitContent := fmt.Sprintf(systemdUnit, binPath)
	unitPath := "/etc/systemd/system/gamejanitor.service"

	if os.Getuid() != 0 {
		return exitError(fmt.Errorf("installing a systemd service requires root\n  Run: sudo gamejanitor install"))
	}

	if err := os.MkdirAll("/var/lib/gamejanitor", 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	if err := os.WriteFile(unitPath, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("writing service file: %w", err)
	}
	fmt.Printf("Service file written to %s\n", unitPath)

	// Reload systemd, enable and start
	for _, cmdArgs := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "gamejanitor"},
		{"systemctl", "start", "gamejanitor"},
	} {
		c := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("running %s: %w", strings.Join(cmdArgs, " "), err)
		}
	}

	fmt.Println("Gamejanitor installed and started.")
	fmt.Println("  Status:  systemctl status gamejanitor")
	fmt.Println("  Logs:    journalctl -u gamejanitor -f")
	fmt.Println("  Stop:    sudo systemctl stop gamejanitor")
	fmt.Println("  Remove:  sudo systemctl disable gamejanitor && sudo rm " + unitPath)
	return nil
}

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.gamejanitor</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/gamejanitor.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/gamejanitor.log</string>
</dict>
</plist>
`

func installLaunchd() error {
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	plistContent := fmt.Sprintf(launchdPlist, binPath)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding home directory: %w", err)
	}

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	plistPath := filepath.Join(plistDir, "dev.gamejanitor.plist")
	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}
	fmt.Printf("Plist written to %s\n", plistPath)

	c := exec.Command("launchctl", "load", plistPath)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("loading plist: %w", err)
	}

	fmt.Println("Gamejanitor installed and started.")
	fmt.Println("  Logs:    tail -f /tmp/gamejanitor.log")
	fmt.Println("  Stop:    launchctl unload " + plistPath)
	fmt.Println("  Remove:  launchctl unload " + plistPath + " && rm " + plistPath)
	return nil
}
