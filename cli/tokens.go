package cli

import (
	"fmt"
	"os"

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
	tokensCreateCmd.Flags().String("expires-in", "", "Expiry duration (e.g. 720h, 30d)")
	tokensCreateCmd.Flags().Int("max-gameservers", 0, "Max gameservers this token can create (0 = cannot create)")
	tokensCreateCmd.Flags().Int("max-memory-mb", 0, "Max total memory (MB) across all owned gameservers")
	tokensCreateCmd.Flags().Float64("max-cpu", 0, "Max total CPU across all owned gameservers")
	tokensCreateCmd.Flags().Int("max-storage-mb", 0, "Max total storage (MB) across all owned gameservers")

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
  gamejanitor tokens create --name friend --role user --max-gameservers 3 --max-memory-mb 4096`,
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

		expiresIn, _ := cmd.Flags().GetString("expires-in")

		req := &gamejanitor.CreateTokenRequest{
			Name: name,
			Role: role,
		}
		if expiresIn != "" {
			req.ExpiresIn = expiresIn
		}

		// Quota flags — only set if non-zero (zero means don't set the limit)
		if v, _ := cmd.Flags().GetInt("max-gameservers"); v > 0 {
			req.MaxGameservers = &v
		}
		if v, _ := cmd.Flags().GetInt("max-memory-mb"); v > 0 {
			req.MaxMemoryMB = &v
		}
		if v, _ := cmd.Flags().GetFloat64("max-cpu"); v > 0 {
			req.MaxCPU = &v
		}
		if v, _ := cmd.Flags().GetInt("max-storage-mb"); v > 0 {
			req.MaxStorageMB = &v
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
