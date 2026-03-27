package cli

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/games"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
	"github.com/spf13/cobra"
)

var gamesCmd = &cobra.Command{
	Use:   "games",
	Short: "List available games",
}

func init() {
	gamesCmd.AddCommand(gamesListCmd, gamesGetCmd)
}

var gamesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all games",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Try API first, fall back to local game store if server isn't running
		gameList, err := getClient().Games.List(ctx())
		if err == nil {
			if jsonOutput {
				printJSON(gameList)
				return nil
			}
			w := newTabWriter()
			fmt.Fprintln(w, "ID\tNAME\tALIASES")
			for _, g := range gameList {
				fmt.Fprintf(w, "%s\t%s\t%s\n", g.ID, g.Name, joinStrings(g.Aliases))
			}
			w.Flush()
			return nil
		}

		// Fallback: load embedded game store locally
		store := loadLocalGameStore()
		if store == nil {
			return exitError(err)
		}

		allGames := store.ListGames()
		if jsonOutput {
			printJSON(allGames)
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tALIASES")
		for _, g := range allGames {
			fmt.Fprintf(w, "%s\t%s\t%s\n", g.ID, g.Name, joinStrings(g.Aliases))
		}
		w.Flush()
		return nil
	},
}

var gamesGetCmd = &cobra.Command{
	Use:     "get <id>",
	Short:   "Get game details (ports, env vars, mod support)",
	Example: `  gamejanitor games get mc`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Try API first
		game, err := getClient().Games.Get(ctx(), args[0])
		if err == nil {
			if jsonOutput {
				printJSON(game)
				return nil
			}
			printSDKGameDetails(game)
			return nil
		}

		// Fallback: local game store
		store := loadLocalGameStore()
		if store == nil {
			return exitError(err)
		}

		id := store.ResolveGameID(args[0])
		g := store.GetGame(id)
		if g == nil {
			return exitError(fmt.Errorf("game %q not found", args[0]))
		}

		if jsonOutput {
			printJSON(g)
			return nil
		}

		printLocalGameDetails(g)
		return nil
	},
}

func loadLocalGameStore() *games.GameStore {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := config.DefaultConfig().DataDir
	store, err := games.NewGameStore(dataDir+"/games", log)
	if err != nil {
		return nil
	}
	return store
}

func printSDKGameDetails(g *gamejanitor.Game) {
	fmt.Printf("ID:                  %s\n", g.ID)
	fmt.Printf("Name:                %s\n", g.Name)
	if g.Description != "" {
		fmt.Printf("Description:         %s\n", g.Description)
	}
	if len(g.Aliases) > 0 {
		fmt.Printf("Aliases:             %s\n", joinStrings(g.Aliases))
	}
	fmt.Printf("Image:               %s\n", g.BaseImage)
	fmt.Printf("Recommended Memory:  %s\n", formatMemory(g.RecommendedMemoryMB))

	if len(g.DefaultPorts) > 0 {
		fmt.Println()
		fmt.Println("Ports:")
		for _, p := range g.DefaultPorts {
			fmt.Printf("  %-10s %d/%s\n", p.Name, p.Port, p.Protocol)
		}
	}

	// Show user-configurable env vars (not system/hidden)
	var visible []gamejanitor.GameEnvVar
	for _, e := range g.DefaultEnv {
		if !e.System && !e.Hidden {
			visible = append(visible, e)
		}
	}
	if len(visible) > 0 {
		fmt.Println()
		fmt.Println("Settings (--env KEY=VALUE):")
		w := newTabWriter()
		for _, e := range visible {
			def := e.Default
			if def == "" && e.Autogenerate != "" {
				def = "(auto)"
			} else if def == "" {
				def = "(none)"
			}
			note := ""
			if e.ConsentRequired {
				note = " (required, must accept)"
			} else if e.Required {
				note = " (required)"
			}
			opts := ""
			if len(e.Options) > 0 {
				opts = "[" + joinStrings(e.Options) + "]"
			}
			fmt.Fprintf(w, "  %s\tdefault: %s\t%s%s\n", e.Key, def, opts, note)
		}
		w.Flush()
	}
}

func printLocalGameDetails(g *games.Game) {
	fmt.Printf("ID:                  %s\n", g.ID)
	fmt.Printf("Name:                %s\n", g.Name)
	if g.Description != "" {
		fmt.Printf("Description:         %s\n", g.Description)
	}
	if len(g.Aliases) > 0 {
		fmt.Printf("Aliases:             %s\n", joinStrings(g.Aliases))
	}
	fmt.Printf("Image:               %s\n", g.BaseImage)
	fmt.Printf("Recommended Memory:  %s\n", formatMemory(g.RecommendedMemoryMB))

	if len(g.DefaultPorts) > 0 {
		fmt.Println()
		fmt.Println("Ports:")
		for _, p := range g.DefaultPorts {
			fmt.Printf("  %-10s %d/%s\n", p.Name, p.Port, p.Protocol)
		}
	}

	var visible []games.EnvVar
	for _, e := range g.DefaultEnv {
		if !e.System && !e.Hidden {
			visible = append(visible, e)
		}
	}
	if len(visible) > 0 {
		fmt.Println()
		fmt.Println("Settings (--env KEY=VALUE):")
		w := newTabWriter()
		for _, e := range visible {
			def := e.Default
			if def == "" && e.Autogenerate != "" {
				def = "(auto)"
			} else if def == "" {
				def = "(none)"
			}
			note := ""
			if e.ConsentRequired {
				note = " (required, must accept)"
			} else if e.Required {
				note = " (required)"
			}
			opts := ""
			if len(e.Options) > 0 {
				opts = "[" + strings.Join(e.Options, ", ") + "]"
			}
			fmt.Fprintf(w, "  %s\tdefault: %s\t%s%s\n", e.Key, def, opts, note)
		}
		w.Flush()
	}
}

func joinStrings(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
