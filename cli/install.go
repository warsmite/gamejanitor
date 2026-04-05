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
	Use:   "install",
	Short: "Install as a system service",
	Long:  "Installs gamejanitor as a system service (systemd on Linux, launchd on macOS) so it starts on boot and restarts on crash.",
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().String("data-dir", "", "Data directory (default: /var/lib/gamejanitor on Linux, ~/Library/Application Support/gamejanitor on macOS)")
}

func runInstall(cmd *cobra.Command, args []string) error {
	dataDir, _ := cmd.Flags().GetString("data-dir")

	if runtime.GOOS == "darwin" {
		if dataDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("finding home directory: %w", err)
			}
			dataDir = filepath.Join(home, "Library", "Application Support", "gamejanitor")
		}
		return installLaunchd(dataDir)
	}

	if os.Getuid() != 0 {
		return exitError(fmt.Errorf("installing a systemd service requires root\n  Run: sudo gamejanitor install"))
	}

	if _, err := exec.LookPath("systemctl"); err != nil {
		return exitError(fmt.Errorf("systemd not found — gamejanitor install requires systemd on Linux"))
	}

	if dataDir == "" {
		dataDir = "/var/lib/gamejanitor"
	}

	return installSystemd(dataDir)
}

func installSystemd(dataDir string) error {
	binPath, err := resolveExecutable()
	if err != nil {
		return err
	}

	execStart := fmt.Sprintf("%s serve -d %s", binPath, dataDir)
	afterUnits := "network-online.target"

	unitContent := fmt.Sprintf(`[Unit]
Description=Gamejanitor - Game Server Manager
After=%s
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5

NoNewPrivileges=true
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`, afterUnits, execStart, dataDir)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	unitPath := "/etc/systemd/system/gamejanitor.service"
	if err := os.WriteFile(unitPath, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("writing service file: %w", err)
	}
	fmt.Printf("Service file written to %s\n", unitPath)

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
	fmt.Printf("  Data dir: %s\n", dataDir)
	fmt.Println("  Status:   systemctl status gamejanitor")
	fmt.Println("  Logs:     journalctl -u gamejanitor -f")
	fmt.Println("  Stop:     sudo systemctl stop gamejanitor")
	fmt.Println("  Remove:   sudo systemctl disable gamejanitor && sudo rm " + unitPath)
	return nil
}

func installLaunchd(dataDir string) error {
	binPath, err := resolveExecutable()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	logPath := filepath.Join(dataDir, "gamejanitor.log")

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.gamejanitor</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
        <string>-d</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, binPath, dataDir, logPath, logPath)

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
	fmt.Printf("  Data dir: %s\n", dataDir)
	fmt.Printf("  Logs:     tail -f %s\n", logPath)
	fmt.Println("  Stop:     launchctl unload " + plistPath)
	fmt.Println("  Remove:   launchctl unload " + plistPath + " && rm " + plistPath)
	return nil
}

func resolveExecutable() (string, error) {
	binPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding executable path: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return "", fmt.Errorf("resolving executable path: %w", err)
	}
	return binPath, nil
}
