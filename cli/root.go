package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	jsonOutput       bool
	apiURL           string
	authToken        string
	clusterName      string
	skipConfirmation bool
)

var rootCmd = &cobra.Command{
	Use:   "gamejanitor",
	Short: "Game Server Manager",
	Long:  "Gamejanitor — Game Server Manager",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "http://localhost:8080", "API base URL")
	rootCmd.PersistentFlags().StringVar(&authToken, "token", "", "Auth token")
	rootCmd.PersistentFlags().StringVar(&clusterName, "cluster", "", "Use a specific cluster context")
	rootCmd.PersistentFlags().BoolVarP(&skipConfirmation, "yes", "y", false, "Skip confirmation prompts")

	rootCmd.SetHelpFunc(customHelp)
	rootCmd.SetUsageFunc(customUsage)

	// Gameserver commands (top-level shortcuts)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(commandCmd)
	rootCmd.AddCommand(updateGameCmd)
	rootCmd.AddCommand(reinstallCmd)
	rootCmd.AddCommand(migrateCmd)

	// Resource management
	rootCmd.AddCommand(gameserversCmd)
	rootCmd.AddCommand(backupsCmd)
	rootCmd.AddCommand(schedulesCmd)
	rootCmd.AddCommand(gamesCmd)

	// Administration
	rootCmd.AddCommand(tokensCmd)
	rootCmd.AddCommand(tokenCmd)
	rootCmd.AddCommand(workersCmd)
	rootCmd.AddCommand(settingsCmd)
	rootCmd.AddCommand(webhooksCmd)
	rootCmd.AddCommand(eventsCmd)

	// Server
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(genWorkerCertCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(initConfigCmd)
	rootCmd.AddCommand(clusterCmd)

	registerCompletions()
}

func Execute() error {
	return rootCmd.Execute()
}

// Styles for help output
var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cmdStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	descStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	flagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	hintStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

func customHelp(cmd *cobra.Command, args []string) {
	if cmd != rootCmd {
		// For subcommands, use Cobra's default help generation
		customUsage(cmd)
		return
	}

	noColor := os.Getenv("NO_COLOR") != ""

	title := "Gamejanitor — Game Server Manager"
	if !noColor {
		title = titleStyle.Render(title)
	}
	fmt.Fprintln(os.Stdout, title)
	fmt.Fprintln(os.Stdout)

	sections := []struct {
		name     string
		commands []struct{ name, desc string }
	}{
		{
			name: "Gameserver Commands",
			commands: []struct{ name, desc string }{
				{"create", "Create a new gameserver"},
				{"delete", "Delete a gameserver"},
				{"start", "Start a gameserver"},
				{"stop", "Stop a gameserver"},
				{"restart", "Restart a gameserver"},
				{"status", "Show gameserver or cluster status"},
				{"logs", "Show gameserver or service logs"},
				{"command", "Send a console command to a gameserver"},
				{"update-game", "Update a gameserver's game version"},
				{"reinstall", "Reinstall a gameserver"},
				{"migrate", "Migrate a gameserver to another node"},
			},
		},
		{
			name: "Resource Management",
			commands: []struct{ name, desc string }{
				{"gameservers", "Manage gameservers                    (aliases: gs)"},
				{"backups", "Manage backups"},
				{"schedules", "Manage scheduled tasks"},
				{"games", "List available games"},
			},
		},
		{
			name: "Administration",
			commands: []struct{ name, desc string }{
				{"tokens", "Manage auth tokens"},
				{"workers", "Manage worker nodes                   (aliases: w)"},
				{"settings", "View and configure settings"},
				{"webhooks", "Manage webhook endpoints"},
				{"events", "Query or stream events"},
			},
		},
		{
			name: "Server",
			commands: []struct{ name, desc string }{
				{"serve", "Start the gamejanitor server"},
				{"install", "Install as a system service"},
				{"update", "Self-update to latest release"},
				{"init", "Generate a starter config file"},
				{"cluster", "Manage cluster connections             (aliases: ctx)"},
				{"completion", "Generate shell completions"},
			},
		},
	}

	for _, section := range sections {
		header := section.name + ":"
		if !noColor {
			header = headerStyle.Render(header)
		}
		fmt.Fprintln(os.Stdout, header)
		for _, cmd := range section.commands {
			name := fmt.Sprintf("  %-14s", cmd.name)
			desc := cmd.desc
			if !noColor {
				name = cmdStyle.Render(name)
				desc = descStyle.Render(desc)
			}
			fmt.Fprintf(os.Stdout, "%s  %s\n", name, desc)
		}
		fmt.Fprintln(os.Stdout)
	}

	// Get started — at the bottom so it's always visible in the terminal
	header := "Get started:"
	if !noColor {
		header = headerStyle.Render(header)
	}
	fmt.Fprintln(os.Stdout, header)
	fmt.Fprintf(os.Stdout, "  %-14s  %s\n", "gamejanitor serve", "Start the server")
	hint := "  http://localhost:8080                 Open the web UI"
	if !noColor {
		hint = hintStyle.Render(hint)
	}
	fmt.Fprintln(os.Stdout, hint)
	fmt.Fprintln(os.Stdout)

	// Flags
	header = "Flags:"
	if !noColor {
		header = headerStyle.Render(header)
	}
	fmt.Fprintln(os.Stdout, header)
	flags := []struct{ name, desc string }{
		{"--json", "Output as JSON"},
		{"--yes, -y", "Skip confirmation prompts"},
		{"--help, -h", "Show help"},
	}
	for _, f := range flags {
		name := fmt.Sprintf("  %-14s", f.name)
		desc := f.desc
		if !noColor {
			name = flagStyle.Render(name)
		}
		fmt.Fprintf(os.Stdout, "%s  %s\n", name, desc)
	}
	fmt.Fprintln(os.Stdout)

	footer := "Run 'gamejanitor <command> --help' for details."
	if !noColor {
		footer = hintStyle.Render(footer)
	}
	fmt.Fprintln(os.Stdout, footer)
}

func customUsage(cmd *cobra.Command) error {
	if cmd == rootCmd {
		customHelp(cmd, nil)
		return nil
	}

	// Build a clean usage string for subcommands
	var b strings.Builder
	fmt.Fprintf(&b, "Usage:\n  %s\n", cmd.UseLine())

	if cmd.HasAvailableSubCommands() {
		fmt.Fprintln(&b, "\nAvailable Commands:")
		for _, sub := range cmd.Commands() {
			if !sub.Hidden {
				fmt.Fprintf(&b, "  %-14s  %s\n", sub.Name(), sub.Short)
			}
		}
	}

	if cmd.HasAvailableLocalFlags() {
		fmt.Fprintf(&b, "\nFlags:\n%s", cmd.LocalFlags().FlagUsages())
	}

	if cmd.HasAvailableInheritedFlags() {
		fmt.Fprintf(&b, "\nGlobal Flags:\n%s", cmd.InheritedFlags().FlagUsages())
	}

	fmt.Fprint(os.Stdout, b.String())
	return nil
}
