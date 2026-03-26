package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var webhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage webhook endpoints",
}

func init() {
	webhooksCreateCmd.Flags().String("url", "", "Webhook URL (required)")
	webhooksCreateCmd.Flags().StringSlice("events", []string{"*"}, "Event patterns (comma-separated, default: all)")
	webhooksCreateCmd.Flags().String("secret", "", "HMAC-SHA256 signing secret")
	webhooksCreateCmd.Flags().String("description", "", "Description")
	webhooksCreateCmd.MarkFlagRequired("url")

	webhooksUpdateCmd.Flags().String("url", "", "Webhook URL")
	webhooksUpdateCmd.Flags().StringSlice("events", nil, "Event patterns")
	webhooksUpdateCmd.Flags().String("secret", "", "Signing secret")
	webhooksUpdateCmd.Flags().Bool("enabled", false, "Enable or disable")
	webhooksUpdateCmd.Flags().String("description", "", "Description")

	webhooksDeliveriesCmd.Flags().String("state", "", "Filter by state: pending, delivered, failed")
	webhooksDeliveriesCmd.Flags().Int("limit", 50, "Number of deliveries to show")

	webhooksCmd.AddCommand(webhooksListCmd, webhooksCreateCmd, webhooksUpdateCmd, webhooksDeleteCmd, webhooksTestCmd, webhooksDeliveriesCmd)
}

var webhooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List webhook endpoints",
	RunE: func(cmd *cobra.Command, args []string) error {
		webhooks, err := getClient().Webhooks.List(ctx())
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(webhooks)
			return nil
		}

		if len(webhooks) == 0 {
			fmt.Println("No webhooks configured.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tURL\tEVENTS\tENABLED\tDESCRIPTION")
		for _, wh := range webhooks {
			enabled := "yes"
			if !wh.Enabled {
				enabled = "no"
			}
			events := strings.Join(wh.Events, ", ")
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", wh.ID[:8], wh.URL, events, enabled, wh.Description)
		}
		w.Flush()
		return nil
	},
}

var webhooksCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a webhook endpoint",
	RunE: func(cmd *cobra.Command, args []string) error {
		webhookURL, _ := cmd.Flags().GetString("url")
		events, _ := cmd.Flags().GetStringSlice("events")
		secret, _ := cmd.Flags().GetString("secret")
		description, _ := cmd.Flags().GetString("description")

		req := &gamejanitor.CreateWebhookRequest{
			URL:         webhookURL,
			Events:      events,
			Description: description,
		}
		if secret != "" {
			req.Secret = secret
		}

		wh, err := getClient().Webhooks.Create(ctx(), req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wh)
			return nil
		}

		fmt.Printf("Webhook created: %s (%s)\n", wh.URL, wh.ID[:8])
		return nil
	},
}

var webhooksUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a webhook endpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := &gamejanitor.UpdateWebhookRequest{}
		hasUpdate := false

		if cmd.Flags().Changed("url") {
			v, _ := cmd.Flags().GetString("url")
			req.URL = gamejanitor.Ptr(v)
			hasUpdate = true
		}
		if cmd.Flags().Changed("events") {
			v, _ := cmd.Flags().GetStringSlice("events")
			req.Events = v
			hasUpdate = true
		}
		if cmd.Flags().Changed("secret") {
			v, _ := cmd.Flags().GetString("secret")
			req.Secret = gamejanitor.Ptr(v)
			hasUpdate = true
		}
		if cmd.Flags().Changed("enabled") {
			v, _ := cmd.Flags().GetBool("enabled")
			req.Enabled = gamejanitor.Ptr(v)
			hasUpdate = true
		}
		if cmd.Flags().Changed("description") {
			v, _ := cmd.Flags().GetString("description")
			req.Description = gamejanitor.Ptr(v)
			hasUpdate = true
		}

		if !hasUpdate {
			return exitError(fmt.Errorf("no update flags specified"))
		}

		wh, err := getClient().Webhooks.Update(ctx(), args[0], req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wh)
			return nil
		}

		fmt.Printf("Webhook %s updated.\n", args[0][:8])
		return nil
	},
}

var webhooksDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a webhook endpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction(fmt.Sprintf("Delete webhook %s?", args[0][:8])) {
			fmt.Println("Aborted.")
			return nil
		}

		err := getClient().Webhooks.Delete(ctx(), args[0])
		if err != nil {
			return exitError(err)
		}

		if !jsonOutput {
			fmt.Printf("Webhook %s deleted.\n", args[0][:8])
		}
		return nil
	},
}

var webhooksTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send a test delivery to a webhook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().Webhooks.Test(ctx(), args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(result)
			return nil
		}

		fmt.Printf("Test delivery sent to webhook %s.\n", args[0][:8])
		return nil
	},
}

var webhooksDeliveriesCmd = &cobra.Command{
	Use:   "deliveries <id>",
	Short: "List deliveries for a webhook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		state, _ := cmd.Flags().GetString("state")

		opts := &gamejanitor.DeliveryListOptions{
			Limit: limit,
			State: state,
		}

		deliveries, err := getClient().Webhooks.Deliveries(ctx(), args[0], opts)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(deliveries)
			return nil
		}

		if len(deliveries) == 0 {
			fmt.Println("No deliveries found.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tEVENT\tSTATE\tTIME")
		for _, d := range deliveries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID[:8], d.EventType, d.State, d.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		w.Flush()
		return nil
	},
}
