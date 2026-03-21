package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Manage gamejanitor settings",
}

var settingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show current settings",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/settings")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

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
		if err := json.Unmarshal(resp.Data, &s); err != nil {
			return fmt.Errorf("parsing response: %w", err)
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
	},
}

var settingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update settings (only provided flags are changed)",
	RunE: func(cmd *cobra.Command, args []string) error {
		body := make(map[string]any)

		if cmd.Flags().Changed("connection-address") {
			v, _ := cmd.Flags().GetString("connection-address")
			body["connection_address"] = v
		}
		if cmd.Flags().Changed("port-range-start") {
			v, _ := cmd.Flags().GetInt("port-range-start")
			body["port_range_start"] = v
		}
		if cmd.Flags().Changed("port-range-end") {
			v, _ := cmd.Flags().GetInt("port-range-end")
			body["port_range_end"] = v
		}
		if cmd.Flags().Changed("port-mode") {
			v, _ := cmd.Flags().GetString("port-mode")
			body["port_mode"] = v
		}
		if cmd.Flags().Changed("max-backups") {
			v, _ := cmd.Flags().GetInt("max-backups")
			body["max_backups"] = v
		}
		if cmd.Flags().Changed("auth-enabled") {
			v, _ := cmd.Flags().GetString("auth-enabled")
			b, err := strconv.ParseBool(v)
			if err != nil {
				return exitError(fmt.Errorf("invalid --auth-enabled value: use true or false"))
			}
			body["auth_enabled"] = b
		}
		if cmd.Flags().Changed("localhost-bypass") {
			v, _ := cmd.Flags().GetString("localhost-bypass")
			b, err := strconv.ParseBool(v)
			if err != nil {
				return exitError(fmt.Errorf("invalid --localhost-bypass value: use true or false"))
			}
			body["localhost_bypass"] = b
		}
		if cmd.Flags().Changed("webhook-enabled") {
			v, _ := cmd.Flags().GetString("webhook-enabled")
			b, err := strconv.ParseBool(v)
			if err != nil {
				return exitError(fmt.Errorf("invalid --webhook-enabled value: use true or false"))
			}
			body["webhook_enabled"] = b
		}
		if cmd.Flags().Changed("webhook-url") {
			v, _ := cmd.Flags().GetString("webhook-url")
			body["webhook_url"] = v
		}
		if cmd.Flags().Changed("webhook-secret") {
			v, _ := cmd.Flags().GetString("webhook-secret")
			body["webhook_secret"] = v
		}

		if len(body) == 0 {
			return exitError(fmt.Errorf("no settings flags provided — use --help to see available options"))
		}

		resp, err := apiPatch("/api/settings", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Updated %d setting(s).\n", len(body))
		return nil
	},
}

func init() {
	settingsSetCmd.Flags().String("connection-address", "", "Public connection address (empty to clear)")
	settingsSetCmd.Flags().Int("port-range-start", 0, "Start of gameserver port range")
	settingsSetCmd.Flags().Int("port-range-end", 0, "End of gameserver port range")
	settingsSetCmd.Flags().String("port-mode", "", "Port allocation mode: auto or manual")
	settingsSetCmd.Flags().Int("max-backups", 0, "Max backups per gameserver (0 = unlimited)")
	settingsSetCmd.Flags().String("auth-enabled", "", "Enable authentication: true or false")
	settingsSetCmd.Flags().String("localhost-bypass", "", "Localhost auth bypass: true or false")
	settingsSetCmd.Flags().String("webhook-enabled", "", "Enable webhooks: true or false")
	settingsSetCmd.Flags().String("webhook-url", "", "Webhook URL (empty to clear)")
	settingsSetCmd.Flags().String("webhook-secret", "", "Webhook HMAC secret (empty to clear)")

	settingsCmd.AddCommand(settingsGetCmd, settingsSetCmd)
}
