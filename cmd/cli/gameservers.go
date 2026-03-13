package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var gameserversCmd = &cobra.Command{
	Use:     "gameservers",
	Aliases: []string{"gs"},
	Short:   "Manage gameservers",
}

var gameserversListCmd = &cobra.Command{
	Use:   "list",
	Short: "List gameservers",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/api/gameservers"
		params := url.Values{}
		if v, _ := cmd.Flags().GetString("game"); v != "" {
			params.Set("game", v)
		}
		if v, _ := cmd.Flags().GetString("status"); v != "" {
			params.Set("status", v)
		}
		if len(params) > 0 {
			path += "?" + params.Encode()
		}

		resp, err := apiGet(path)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var gameservers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			GameID string `json:"game_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &gameservers); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tGAME\tSTATUS")
		for _, gs := range gameservers {
			id := gs.ID
			if len(id) > 8 {
				id = id[:8]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, gs.Name, gs.GameID, gs.Status)
		}
		w.Flush()
		return nil
	},
}

var gameserversGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a gameserver by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/gameservers/" + args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var gs struct {
			ID            string          `json:"id"`
			Name          string          `json:"name"`
			GameID        string          `json:"game_id"`
			Status        string          `json:"status"`
			MemoryLimitMB int             `json:"memory_limit_mb"`
			CPULimit      float64         `json:"cpu_limit"`
			AutoStart     bool            `json:"auto_start"`
			VolumeName    string          `json:"volume_name"`
			Ports         json.RawMessage `json:"ports"`
			Env           json.RawMessage `json:"env"`
		}
		if err := json.Unmarshal(resp.Data, &gs); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("ID:         %s\n", gs.ID)
		fmt.Printf("Name:       %s\n", gs.Name)
		fmt.Printf("Game:       %s\n", gs.GameID)
		fmt.Printf("Status:     %s\n", gs.Status)
		fmt.Printf("Memory:     %d MB\n", gs.MemoryLimitMB)
		fmt.Printf("CPU:        %.1f\n", gs.CPULimit)
		fmt.Printf("Auto Start: %v\n", gs.AutoStart)
		fmt.Printf("Volume:     %s\n", gs.VolumeName)
		fmt.Printf("Ports:      %s\n", string(gs.Ports))
		fmt.Printf("Env:        %s\n", string(gs.Env))
		return nil
	},
}

var gameserversCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new gameserver",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		gameID, _ := cmd.Flags().GetString("game")
		portFlags, _ := cmd.Flags().GetStringSlice("port")
		envFlags, _ := cmd.Flags().GetStringSlice("env")
		memory, _ := cmd.Flags().GetInt("memory")
		cpu, _ := cmd.Flags().GetFloat64("cpu")
		autoStart, _ := cmd.Flags().GetBool("auto-start")

		if name == "" || gameID == "" {
			return exitError(fmt.Errorf("--name and --game are required"))
		}

		ports, err := parsePorts(portFlags)
		if err != nil {
			return exitError(err)
		}

		env := parseEnvFlags(envFlags)

		body := map[string]any{
			"name":            name,
			"game_id":         gameID,
			"ports":           ports,
			"env":             env,
			"memory_limit_mb": memory,
			"cpu_limit":       cpu,
			"auto_start":      autoStart,
		}

		resp, err := apiPost("/api/gameservers", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var gs struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(resp.Data, &gs); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		fmt.Printf("Gameserver %s created (id: %s).\n", gs.Name, gs.ID)
		return nil
	},
}

var gameserversUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a gameserver (must be stopped)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"id": args[0]}

		if cmd.Flags().Changed("name") {
			v, _ := cmd.Flags().GetString("name")
			body["name"] = v
		}
		if cmd.Flags().Changed("port") {
			portFlags, _ := cmd.Flags().GetStringSlice("port")
			ports, err := parsePorts(portFlags)
			if err != nil {
				return exitError(err)
			}
			body["ports"] = ports
		}
		if cmd.Flags().Changed("env") {
			envFlags, _ := cmd.Flags().GetStringSlice("env")
			body["env"] = parseEnvFlags(envFlags)
		}
		if cmd.Flags().Changed("memory") {
			v, _ := cmd.Flags().GetInt("memory")
			body["memory_limit_mb"] = v
		}
		if cmd.Flags().Changed("cpu") {
			v, _ := cmd.Flags().GetFloat64("cpu")
			body["cpu_limit"] = v
		}
		if cmd.Flags().Changed("auto-start") {
			v, _ := cmd.Flags().GetBool("auto-start")
			body["auto_start"] = v
		}

		resp, err := apiPut("/api/gameservers/"+args[0], body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Gameserver %s updated.\n", args[0])
		return nil
	},
}

var gameserversDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := apiDelete("/api/gameservers/" + args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(&apiResponse{Status: "ok"})
			return nil
		}

		fmt.Printf("Gameserver %s deleted.\n", args[0])
		return nil
	},
}

// parsePorts parses --port flags in format "name:host_port:container_port/protocol"
func parsePorts(flags []string) ([]map[string]any, error) {
	var ports []map[string]any
	for _, f := range flags {
		// Split protocol from the rest
		proto := "tcp"
		if idx := strings.LastIndex(f, "/"); idx != -1 {
			proto = f[idx+1:]
			f = f[:idx]
		}

		parts := strings.SplitN(f, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid port format %q, expected name:host_port:container_port/protocol", f)
		}

		hostPort, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid host port %q: %w", parts[1], err)
		}
		containerPort, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("invalid container port %q: %w", parts[2], err)
		}

		ports = append(ports, map[string]any{
			"name":           parts[0],
			"host_port":      hostPort,
			"container_port": containerPort,
			"protocol":       proto,
		})
	}
	return ports, nil
}

// parseEnvFlags parses --env flags in format "KEY=VALUE"
func parseEnvFlags(flags []string) map[string]string {
	env := make(map[string]string)
	for _, f := range flags {
		if idx := strings.IndexByte(f, '='); idx != -1 {
			env[f[:idx]] = f[idx+1:]
		}
	}
	return env
}

func init() {
	gameserversListCmd.Flags().String("game", "", "Filter by game ID")
	gameserversListCmd.Flags().String("status", "", "Filter by status")

	gameserversCreateCmd.Flags().String("name", "", "Gameserver name")
	gameserversCreateCmd.Flags().String("game", "", "Game ID")
	gameserversCreateCmd.Flags().StringSlice("port", nil, "Port mapping (name:host:container/proto)")
	gameserversCreateCmd.Flags().StringSlice("env", nil, "Environment variable (KEY=VALUE)")
	gameserversCreateCmd.Flags().Int("memory", 0, "Memory limit (MB)")
	gameserversCreateCmd.Flags().Float64("cpu", 0, "CPU limit")
	gameserversCreateCmd.Flags().Bool("auto-start", false, "Auto-start on Gamejanitor startup")

	gameserversUpdateCmd.Flags().String("name", "", "Gameserver name")
	gameserversUpdateCmd.Flags().StringSlice("port", nil, "Port mapping (name:host:container/proto)")
	gameserversUpdateCmd.Flags().StringSlice("env", nil, "Environment variable (KEY=VALUE)")
	gameserversUpdateCmd.Flags().Int("memory", 0, "Memory limit (MB)")
	gameserversUpdateCmd.Flags().Float64("cpu", 0, "CPU limit")
	gameserversUpdateCmd.Flags().Bool("auto-start", false, "Auto-start on Gamejanitor startup")

	gameserversCmd.AddCommand(
		gameserversListCmd, gameserversGetCmd, gameserversCreateCmd,
		gameserversUpdateCmd, gameserversDeleteCmd,
	)
}
