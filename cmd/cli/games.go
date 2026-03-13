package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var gamesCmd = &cobra.Command{
	Use:   "games",
	Short: "Manage game definitions",
}

var gamesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all games",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/games")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var games []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Image string `json:"image"`
		}
		if err := json.Unmarshal(resp.Data, &games); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tIMAGE")
		for _, g := range games {
			fmt.Fprintf(w, "%s\t%s\t%s\n", g.ID, g.Name, g.Image)
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
		resp, err := apiGet("/api/games/" + args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var game struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Image       string  `json:"image"`
			MinMemoryMB int     `json:"min_memory_mb"`
			MinCPU      float64 `json:"min_cpu"`
		}
		if err := json.Unmarshal(resp.Data, &game); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("ID:         %s\n", game.ID)
		fmt.Printf("Name:       %s\n", game.Name)
		fmt.Printf("Image:      %s\n", game.Image)
		fmt.Printf("Min Memory: %d MB\n", game.MinMemoryMB)
		fmt.Printf("Min CPU:    %.1f\n", game.MinCPU)
		return nil
	},
}

var gamesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new game definition",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		name, _ := cmd.Flags().GetString("name")
		image, _ := cmd.Flags().GetString("image")
		defaultPorts, _ := cmd.Flags().GetString("default-ports")
		defaultEnv, _ := cmd.Flags().GetString("default-env")
		minMemory, _ := cmd.Flags().GetInt("min-memory")
		minCPU, _ := cmd.Flags().GetFloat64("min-cpu")

		if id == "" || name == "" || image == "" {
			return exitError(fmt.Errorf("--id, --name, and --image are required"))
		}

		body := map[string]any{
			"id":            id,
			"name":          name,
			"image":         image,
			"min_memory_mb": minMemory,
			"min_cpu":       minCPU,
		}
		if defaultPorts != "" {
			body["default_ports"] = json.RawMessage(defaultPorts)
		}
		if defaultEnv != "" {
			body["default_env"] = json.RawMessage(defaultEnv)
		}

		resp, err := apiPost("/api/games", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Game %s created.\n", id)
		return nil
	},
}

var gamesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a game definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"id": args[0]}

		if cmd.Flags().Changed("name") {
			v, _ := cmd.Flags().GetString("name")
			body["name"] = v
		}
		if cmd.Flags().Changed("image") {
			v, _ := cmd.Flags().GetString("image")
			body["image"] = v
		}
		if cmd.Flags().Changed("min-memory") {
			v, _ := cmd.Flags().GetInt("min-memory")
			body["min_memory_mb"] = v
		}
		if cmd.Flags().Changed("min-cpu") {
			v, _ := cmd.Flags().GetFloat64("min-cpu")
			body["min_cpu"] = v
		}

		resp, err := apiPut("/api/games/"+args[0], body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Game %s updated.\n", args[0])
		return nil
	},
}

var gamesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a game definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := apiDelete("/api/games/" + args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(&apiResponse{Status: "ok"})
			return nil
		}

		fmt.Printf("Game %s deleted.\n", args[0])
		return nil
	},
}

func init() {
	gamesCreateCmd.Flags().String("id", "", "Game ID (slug)")
	gamesCreateCmd.Flags().String("name", "", "Display name")
	gamesCreateCmd.Flags().String("image", "", "Docker image")
	gamesCreateCmd.Flags().String("default-ports", "", "Default ports JSON")
	gamesCreateCmd.Flags().String("default-env", "", "Default env JSON")
	gamesCreateCmd.Flags().Int("min-memory", 0, "Minimum memory (MB)")
	gamesCreateCmd.Flags().Float64("min-cpu", 0, "Minimum CPU cores")

	gamesUpdateCmd.Flags().String("name", "", "Display name")
	gamesUpdateCmd.Flags().String("image", "", "Docker image")
	gamesUpdateCmd.Flags().Int("min-memory", 0, "Minimum memory (MB)")
	gamesUpdateCmd.Flags().Float64("min-cpu", 0, "Minimum CPU cores")

	gamesCmd.AddCommand(gamesListCmd, gamesGetCmd, gamesCreateCmd, gamesUpdateCmd, gamesDeleteCmd)
}
