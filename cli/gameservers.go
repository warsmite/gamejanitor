package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// --- List ---

var lsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List gameservers",
	RunE:    runGameserversList,
}

func init() {
	lsCmd.Flags().String("game", "", "Filter by game ID")
	lsCmd.Flags().String("status", "", "Filter by status")
}

func runGameserversList(cmd *cobra.Command, args []string) error {
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

	if len(gameservers) == 0 {
		fmt.Println("No gameservers found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "ID\tNAME\tGAME\tSTATUS")
	for _, gs := range gameservers {
		id := gs.ID
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, gs.Name, gs.GameID, colorStatus(gs.Status))
	}
	w.Flush()
	return nil
}

// --- Get ---

var getCmd = &cobra.Command{
	Use:   "get <name-or-id>",
	Short: "Show gameserver details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		resp, err := apiGet("/api/gameservers/" + gsID)
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
			VolumeName    string          `json:"volume_name"`
			Ports         json.RawMessage `json:"ports"`
			Env           json.RawMessage `json:"env"`
			AutoRestart   bool            `json:"auto_restart"`
			SFTPUsername  string          `json:"sftp_username"`
			SFTPPassword  string          `json:"sftp_password"`
		}
		if err := json.Unmarshal(resp.Data, &gs); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("ID:         %s\n", gs.ID)
		fmt.Printf("Name:       %s\n", gs.Name)
		fmt.Printf("Game:       %s\n", gs.GameID)
		fmt.Printf("Status:     %s\n", colorStatus(gs.Status))
		fmt.Printf("Memory:     %s\n", formatMemory(gs.MemoryLimitMB))
		if gs.CPULimit > 0 {
			fmt.Printf("CPU:        %.1f cores\n", gs.CPULimit)
		} else {
			fmt.Printf("CPU:        unlimited\n")
		}
		fmt.Printf("Restart:    %v\n", gs.AutoRestart)
		fmt.Printf("Volume:     %s\n", gs.VolumeName)
		fmt.Printf("Ports:      %s\n", string(gs.Ports))
		fmt.Printf("Env:        %s\n", string(gs.Env))
		if gs.SFTPUsername != "" {
			fmt.Printf("SFTP User:  %s\n", gs.SFTPUsername)
			fmt.Printf("SFTP Pass:  %s\n", gs.SFTPPassword)
		}
		return nil
	},
}

// --- Create ---

var createCmd = &cobra.Command{
	Use:   "create <name> <game>",
	Short: "Create a new gameserver",
	Args:  cobra.ExactArgs(2),
	RunE:  runCreate,
}

func init() {
	createCmd.Flags().StringSlice("port", nil, "Port mapping (name:host:container/proto)")
	createCmd.Flags().StringSlice("env", nil, "Environment variable (KEY=VALUE)")
	createCmd.Flags().String("memory", "", "Memory limit (e.g. 512m, 4g, 2048)")
	createCmd.Flags().Float64("cpu", 0, "CPU limit in cores")
	createCmd.Flags().String("node", "", "Worker node ID for placement")
	createCmd.Flags().Bool("auto-restart", false, "Auto-restart on crash")
}

func runCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	gameID := args[1]
	portFlags, _ := cmd.Flags().GetStringSlice("port")
	envFlags, _ := cmd.Flags().GetStringSlice("env")
	memoryStr, _ := cmd.Flags().GetString("memory")
	cpu, _ := cmd.Flags().GetFloat64("cpu")

	memory, err := parseMemory(memoryStr)
	if err != nil {
		return exitError(err)
	}

	ports, err := parsePorts(portFlags)
	if err != nil {
		return exitError(err)
	}

	env := parseEnvFlags(envFlags)
	nodeID, _ := cmd.Flags().GetString("node")
	autoRestart, _ := cmd.Flags().GetBool("auto-restart")

	body := map[string]any{
		"name":            name,
		"game_id":         gameID,
		"ports":           ports,
		"env":             env,
		"memory_limit_mb": memory,
		"cpu_limit":       cpu,
		"auto_restart":    autoRestart,
	}
	if len(portFlags) == 0 {
		body["port_mode"] = "auto"
	}
	if nodeID != "" {
		body["node_id"] = nodeID
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
		ID           string `json:"id"`
		Name         string `json:"name"`
		SFTPUsername string `json:"sftp_username"`
		SFTPPassword string `json:"sftp_password"`
	}
	if err := json.Unmarshal(resp.Data, &gs); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	fmt.Printf("Gameserver %s created (id: %s).\n", gs.Name, gs.ID)
	if gs.SFTPUsername != "" && gs.SFTPPassword != "" {
		fmt.Printf("SFTP username: %s\n", gs.SFTPUsername)
		fmt.Printf("SFTP password: %s (will not be shown again)\n", gs.SFTPPassword)
	}
	return nil
}

// --- Delete ---

var deleteCmd = &cobra.Command{
	Use:     "delete <name-or-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a gameserver",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		name := gameserverName(gsID)

		if !confirmAction(fmt.Sprintf("Delete gameserver %s?", name)) {
			fmt.Println("Aborted.")
			return nil
		}

		_, err = apiDelete("/api/gameservers/" + gsID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(&apiResponse{Status: "ok"})
			return nil
		}

		fmt.Printf("Gameserver %s deleted.\n", name)
		return nil
	},
}

// --- Edit ---

var editCmd = &cobra.Command{
	Use:   "edit <name-or-id>",
	Short: "Edit a gameserver's configuration (must be stopped)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		body := map[string]any{"id": gsID}

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
			v, _ := cmd.Flags().GetString("memory")
			mb, err := parseMemory(v)
			if err != nil {
				return exitError(err)
			}
			body["memory_limit_mb"] = mb
		}
		if cmd.Flags().Changed("cpu") {
			v, _ := cmd.Flags().GetFloat64("cpu")
			body["cpu_limit"] = v
		}
		if cmd.Flags().Changed("auto-restart") {
			v, _ := cmd.Flags().GetBool("auto-restart")
			body["auto_restart"] = v
		}

		resp, err := apiPatch("/api/gameservers/"+gsID, body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Gameserver %s updated.\n", gameserverName(gsID))
		return nil
	},
}

func init() {
	editCmd.Flags().String("name", "", "Gameserver name")
	editCmd.Flags().StringSlice("port", nil, "Port mapping (name:host:container/proto)")
	editCmd.Flags().StringSlice("env", nil, "Environment variable (KEY=VALUE)")
	editCmd.Flags().String("memory", "", "Memory limit (e.g. 512m, 4g, 2048)")
	editCmd.Flags().Float64("cpu", 0, "CPU limit in cores")
	editCmd.Flags().Bool("auto-restart", false, "Auto-restart on crash")
}

// --- Parsing helpers ---

func parseMemory(s string) (int, error) {
	if s == "" {
		return 0, nil
	}

	s = strings.TrimSpace(strings.ToLower(s))

	if mb, err := strconv.Atoi(s); err == nil {
		return mb, nil
	}

	s = strings.TrimSuffix(s, "b")

	if len(s) < 2 {
		return 0, fmt.Errorf("invalid memory value: %q (use e.g. 512m, 4g, or 2048)", s)
	}

	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory value: %q (use e.g. 512m, 4g, or 2048)", s)
	}

	switch unit {
	case 'm':
		return int(num), nil
	case 'g':
		return int(num * 1024), nil
	default:
		return 0, fmt.Errorf("unknown memory unit %q (use m or g)", string(unit))
	}
}

func parsePorts(flags []string) ([]map[string]any, error) {
	var ports []map[string]any
	for _, f := range flags {
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

func parseEnvFlags(flags []string) map[string]string {
	env := make(map[string]string)
	for _, f := range flags {
		if idx := strings.IndexByte(f, '='); idx != -1 {
			env[f[:idx]] = f[idx+1:]
		}
	}
	return env
}
