package cli

import (
	"fmt"

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
		games, err := getClient().Games.List(ctx())
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(games)
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tIMAGE")
		for _, g := range games {
			fmt.Fprintf(w, "%s\t%s\t%s\n", g.ID, g.Name, g.BaseImage)
		}
		w.Flush()
		return nil
	},
}

var gamesGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a game by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		game, err := getClient().Games.Get(ctx(), args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(game)
			return nil
		}

		fmt.Printf("ID:                  %s\n", game.ID)
		fmt.Printf("Name:                %s\n", game.Name)
		fmt.Printf("Image:               %s\n", game.BaseImage)
		fmt.Printf("Recommended Memory:  %s\n", formatMemory(game.RecommendedMemoryMB))
		return nil
	},
}
