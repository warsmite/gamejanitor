package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/warsmite/gamejanitor/controller/auth"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Manage auth tokens",
}

func init() {
	tokensCreateCmd.Flags().String("name", "", "Token name (required)")
	tokensCreateCmd.Flags().String("role", "user", "Token role: admin, user, or worker")
	tokensCreateCmd.Flags().StringSlice("gameserver", nil, "Grant access to gameserver (repeatable, name or ID)")
	tokensCreateCmd.Flags().StringSlice("permission", nil, "Permission to grant (repeatable). Examples: gameserver.start, gameserver.stop, gameserver.configure.name, backup.read, schedule.read. Run 'gamejanitor tokens permissions' to list all.")
	tokensCreateCmd.Flags().String("expires-in", "", "Expiry duration (e.g. 720h, 30d)")

	tokensListCmd.Flags().String("role", "", "Filter by role: admin, user, or worker")

	tokensRotateCmd.Flags().String("name", "", "Worker token name to rotate (required)")

	tokensCmd.AddCommand(tokensListCmd, tokensCreateCmd, tokensDeleteCmd, tokensRotateCmd, tokensPermissionsCmd)
}

var tokensListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tokens",
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := cmd.Flags().GetString("role")
		tokens, err := getClient().Tokens.List(ctx(), role)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(tokens)
			return nil
		}

		if len(tokens) == 0 {
			fmt.Println("No tokens found.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tROLE\tCREATED\tLAST USED\tEXPIRES")
		for _, t := range tokens {
			lastUsed := "-"
			if t.LastUsedAt != nil {
				lastUsed = t.LastUsedAt.Format("2006-01-02T15:04:05Z")
			}
			expires := "never"
			if t.ExpiresAt != nil {
				expires = t.ExpiresAt.Format("2006-01-02T15:04:05Z")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				t.ID[:8], t.Name, t.Role, t.CreatedAt.Format("2006-01-02T15:04:05Z"), lastUsed, expires)
		}
		w.Flush()
		return nil
	},
}

var tokensCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a token",
	Example: `  gamejanitor tokens create --name admin-key --role admin
  gamejanitor tokens create --name worker-1 --role worker
  gamejanitor tokens create --name panel --role user --gameserver "My Server" --permission start,stop,logs`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return exitError(fmt.Errorf("--name is required"))
		}

		role, _ := cmd.Flags().GetString("role")

		if role == "worker" {
			result, err := getClient().Tokens.Create(ctx(), &gamejanitor.CreateTokenRequest{
				Name: name,
				Role: "worker",
			})
			if err != nil {
				return exitError(err)
			}
			if jsonOutput {
				printJSON(result)
				return nil
			}
			if result.Exists {
				fmt.Fprintf(os.Stderr, "Worker token %q already exists (id: %s)\n", result.Name, result.TokenID)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Worker token %q created (id: %s)\n", result.Name, result.TokenID)
			fmt.Fprintf(os.Stderr, "Store this token — it cannot be retrieved later.\n")
			fmt.Println(result.Token)
			return nil
		}

		gameserverNames, _ := cmd.Flags().GetStringSlice("gameserver")
		permissions, _ := cmd.Flags().GetStringSlice("permission")
		expiresIn, _ := cmd.Flags().GetString("expires-in")

		// Resolve gameserver names to IDs
		var gameserverIDs []string
		for _, gs := range gameserverNames {
			id, err := resolveGameserverID(gs)
			if err != nil {
				return exitError(fmt.Errorf("resolving gameserver %q: %w", gs, err))
			}
			gameserverIDs = append(gameserverIDs, id)
		}

		// Build grants map: each gameserver gets the same permissions
		grants := make(map[string][]string)
		for _, id := range gameserverIDs {
			grants[id] = permissions
		}

		req := &gamejanitor.CreateTokenRequest{
			Name:   name,
			Role:   role,
			Grants: grants,
		}
		if expiresIn != "" {
			req.ExpiresIn = expiresIn
		}

		result, err := getClient().Tokens.Create(ctx(), req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(result)
			return nil
		}

		fmt.Fprintf(os.Stderr, "Token %q created (id: %s)\n", result.Name, result.TokenID)
		if len(gameserverIDs) > 0 {
			fmt.Fprintf(os.Stderr, "Granted access to %d gameserver(s), permissions: %s\n", len(gameserverIDs), strings.Join(permissions, ", "))
		}
		fmt.Fprintf(os.Stderr, "Store this token — it cannot be retrieved later.\n")
		// Raw token to stdout for piping
		fmt.Println(result.Token)
		return nil
	},
}

var tokensDeleteCmd = &cobra.Command{
	Use:   "delete <token-id>",
	Short: "Delete a token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction(fmt.Sprintf("Delete token %s?", args[0])) {
			fmt.Println("Aborted.")
			return nil
		}

		err := getClient().Tokens.Delete(ctx(), args[0])
		if err != nil {
			return exitError(err)
		}

		if !jsonOutput {
			fmt.Println("Token deleted.")
		}
		return nil
	},
}

var tokensPermissionsCmd = &cobra.Command{
	Use:   "permissions",
	Short: "List all valid permission names",
	Run: func(cmd *cobra.Command, args []string) {
		for _, p := range auth.AllPermissions {
			fmt.Println(p)
		}
	},
}

var tokensRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate a worker token (invalidates old, creates new with same name)",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return exitError(fmt.Errorf("--name is required"))
		}

		// Resolve worker token name to ID
		tokens, err := getClient().Tokens.List(ctx(), "worker")
		if err != nil {
			return exitError(err)
		}
		var tokenID string
		for _, t := range tokens {
			if t.Name == name {
				tokenID = t.ID
				break
			}
		}
		if tokenID == "" {
			return exitError(fmt.Errorf("worker token %q not found", name))
		}

		result, err := getClient().Tokens.Rotate(ctx(), tokenID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(result)
			return nil
		}

		fmt.Fprintf(os.Stderr, "Worker token %q rotated (id: %s)\n", name, result.TokenID)
		fmt.Fprintf(os.Stderr, "Old token is now invalid. Store the new token.\n")
		fmt.Println(result.Token)
		return nil
	},
}
