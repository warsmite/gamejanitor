package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "View and configure settings",
	// No args: display settings
	RunE: runSettingsGet,
}

func init() {
	settingsCmd.AddCommand(settingsSetCmd)
}

func runSettingsGet(cmd *cobra.Command, args []string) error {
	settings, err := getClient().Settings.Get(ctx())
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(settings)
		return nil
	}

	// Decode into a typed struct for display
	raw, _ := json.Marshal(settings)
	var s struct {
		ConnectionAddress        string `json:"connection_address"`
		ConnectionAddressFromEnv bool   `json:"connection_address_from_env"`
		PortRangeStart           int    `json:"port_range_start"`
		PortRangeEnd             int    `json:"port_range_end"`
		PortRangeFromEnv         bool   `json:"port_range_from_env"`
		PortMode                 string `json:"port_mode"`
		PortModeFromEnv          bool   `json:"port_mode_from_env"`
		MaxBackups               int    `json:"max_backups"`
		MaxBackupsFromEnv        bool   `json:"max_backups_from_env"`
		AuthEnabled              bool   `json:"auth_enabled"`
		AuthFromEnv              bool   `json:"auth_from_env"`
		LocalhostBypass          bool   `json:"localhost_bypass"`
		LocalhostBypassFromEnv   bool   `json:"localhost_bypass_from_env"`
		WebhookEnabled           bool   `json:"webhook_enabled"`
		WebhookEnabledFromEnv    bool   `json:"webhook_enabled_from_env"`
		WebhookURL               string `json:"webhook_url"`
		WebhookURLFromEnv        bool   `json:"webhook_url_from_env"`
		WebhookSecretSet         bool   `json:"webhook_secret_set"`
		WebhookSecretFromEnv     bool   `json:"webhook_secret_from_env"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("parsing settings: %w", err)
	}

	w := newTabWriter()
	envTag := func(fromEnv bool) string {
		if fromEnv {
			return " (env)"
		}
		return ""
	}

	connAddr := s.ConnectionAddress
	if connAddr == "" {
		connAddr = "(not set)"
	}
	fmt.Fprintf(w, "Connection Address:\t%s%s\n", connAddr, envTag(s.ConnectionAddressFromEnv))
	fmt.Fprintf(w, "Port Range:\t%d-%d%s\n", s.PortRangeStart, s.PortRangeEnd, envTag(s.PortRangeFromEnv))
	fmt.Fprintf(w, "Port Mode:\t%s%s\n", s.PortMode, envTag(s.PortModeFromEnv))
	fmt.Fprintf(w, "Max Backups:\t%d%s\n", s.MaxBackups, envTag(s.MaxBackupsFromEnv))
	fmt.Fprintf(w, "Auth Enabled:\t%v%s\n", s.AuthEnabled, envTag(s.AuthFromEnv))
	fmt.Fprintf(w, "Localhost Bypass:\t%v%s\n", s.LocalhostBypass, envTag(s.LocalhostBypassFromEnv))
	fmt.Fprintf(w, "Webhook Enabled:\t%v%s\n", s.WebhookEnabled, envTag(s.WebhookEnabledFromEnv))
	webhookURL := s.WebhookURL
	if webhookURL == "" {
		webhookURL = "(not set)"
	}
	fmt.Fprintf(w, "Webhook URL:\t%s%s\n", webhookURL, envTag(s.WebhookURLFromEnv))
	secretStatus := "not set"
	if s.WebhookSecretSet {
		secretStatus = "set"
	}
	fmt.Fprintf(w, "Webhook Secret:\t%s%s\n", secretStatus, envTag(s.WebhookSecretFromEnv))
	w.Flush()
	return nil
}

var settingsSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Update a setting",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value := args[1]

		// Map CLI key names to API field names
		keyMap := map[string]string{
			"connection-address": "connection_address",
			"port-range-start":   "port_range_start",
			"port-range-end":     "port_range_end",
			"port-mode":          "port_mode",
			"max-backups":        "max_backups",
			"auth-enabled":       "auth_enabled",
			"localhost-bypass":   "localhost_bypass",
			"webhook-enabled":    "webhook_enabled",
			"webhook-url":        "webhook_url",
			"webhook-secret":     "webhook_secret",
		}

		apiKey, ok := keyMap[key]
		if !ok {
			validKeys := make([]string, 0, len(keyMap))
			for k := range keyMap {
				validKeys = append(validKeys, k)
			}
			return exitError(fmt.Errorf("unknown setting %q\n  Valid keys: %s", key, fmt.Sprintf("%v", validKeys)))
		}

		// Parse value based on key type
		var parsed any
		switch key {
		case "auth-enabled", "localhost-bypass", "webhook-enabled":
			switch value {
			case "true":
				parsed = true
			case "false":
				parsed = false
			default:
				return exitError(fmt.Errorf("invalid value for %s: use true or false", key))
			}
		case "port-range-start", "port-range-end", "max-backups":
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
				return exitError(fmt.Errorf("invalid value for %s: must be a number", key))
			}
			parsed = n
		default:
			parsed = value
		}

		rawValue, _ := json.Marshal(parsed)
		settings := map[string]json.RawMessage{apiKey: rawValue}
		err := getClient().Settings.Update(ctx(), settings)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
			return nil
		}

		fmt.Printf("Setting %s updated.\n", key)
		return nil
	},
}
