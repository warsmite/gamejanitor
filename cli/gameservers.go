package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
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
	opts := &gamejanitor.GameserverListOptions{}
	if v, _ := cmd.Flags().GetString("game"); v != "" {
		opts.Game = v
	}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		opts.Status = v
	}

	resp, err := getClient().Gameservers.List(ctx(), opts)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(resp)
		return nil
	}

	if len(resp.Gameservers) == 0 {
		fmt.Println("No gameservers found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "ID\tNAME\tGAME\tSTATUS")
	for _, gs := range resp.Gameservers {
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

		gs, err := getClient().Gameservers.Get(ctx(), gsID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(gs)
			return nil
		}

		portsJSON, _ := json.Marshal(gs.Ports)
		envJSON, _ := json.Marshal(gs.Env)

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
		autoRestart := false
		if gs.AutoRestart != nil {
			autoRestart = *gs.AutoRestart
		}
		fmt.Printf("Restart:    %v\n", autoRestart)
		fmt.Printf("Connect:    %s\n", cliConnectionAddress(gs.ConnectionAddress, gs.Node, gs.Ports))
		fmt.Printf("Volume:     %s\n", gs.VolumeName)
		fmt.Printf("Ports:      %s\n", string(portsJSON))
		fmt.Printf("Env:        %s\n", string(envJSON))
		if gs.SFTPUsername != "" {
			fmt.Printf("SFTP User:  %s\n", gs.SFTPUsername)
		}
		return nil
	},
}

// --- Create ---

var createCmd = &cobra.Command{
	Use:     "create <name> <game>",
	Short:   "Create a new gameserver",
	Example: `  gamejanitor create "My Server" mc --env EULA=true --memory 2g`,
	Args:    cobra.ExactArgs(2),
	RunE:    runCreate,
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

	ports, err := parsePortMappings(portFlags)
	if err != nil {
		return exitError(err)
	}

	env := parseEnvFlags(envFlags)
	nodeID, _ := cmd.Flags().GetString("node")
	autoRestart, _ := cmd.Flags().GetBool("auto-restart")

	req := &gamejanitor.CreateGameserverRequest{
		Name:          name,
		GameID:        gameID,
		Ports:         ports,
		Env:           env,
		MemoryLimitMB: memory,
		CPULimit:      cpu,
		AutoRestart:   gamejanitor.Ptr(autoRestart),
	}
	if len(portFlags) == 0 {
		req.PortMode = "auto"
	}
	if nodeID != "" {
		req.NodeID = gamejanitor.Ptr(nodeID)
	}

	result, err := getClient().Gameservers.Create(ctx(), req)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(result)
		return nil
	}

	fmt.Printf("Gameserver %s created (id: %s).\n", result.Name, result.ID)
	if result.SFTPUsername != "" && result.SFTPPassword != "" {
		fmt.Printf("SFTP username: %s\n", result.SFTPUsername)
		fmt.Printf("SFTP password: %s (will not be shown again)\n", result.SFTPPassword)
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

		err = getClient().Gameservers.Delete(ctx(), gsID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
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
	Example: `  gamejanitor edit "My Server" --env MAX_PLAYERS=50 --memory 4g`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		req := &gamejanitor.UpdateGameserverRequest{}

		if cmd.Flags().Changed("name") {
			v, _ := cmd.Flags().GetString("name")
			req.Name = gamejanitor.Ptr(v)
		}
		if cmd.Flags().Changed("port") {
			portFlags, _ := cmd.Flags().GetStringSlice("port")
			ports, err := parsePortMappings(portFlags)
			if err != nil {
				return exitError(err)
			}
			req.Ports = ports
		}
		if cmd.Flags().Changed("env") {
			envFlags, _ := cmd.Flags().GetStringSlice("env")
			req.Env = parseEnvFlags(envFlags)
		}
		if cmd.Flags().Changed("memory") {
			v, _ := cmd.Flags().GetString("memory")
			mb, err := parseMemory(v)
			if err != nil {
				return exitError(err)
			}
			req.MemoryLimitMB = gamejanitor.Ptr(mb)
		}
		if cmd.Flags().Changed("cpu") {
			v, _ := cmd.Flags().GetFloat64("cpu")
			req.CPULimit = gamejanitor.Ptr(v)
		}
		if cmd.Flags().Changed("auto-restart") {
			v, _ := cmd.Flags().GetBool("auto-restart")
			req.AutoRestart = gamejanitor.Ptr(v)
		}

		result, err := getClient().Gameservers.Update(ctx(), gsID, req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(result)
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

func parsePortMappings(flags []string) ([]gamejanitor.PortMapping, error) {
	var ports []gamejanitor.PortMapping
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

		ports = append(ports, gamejanitor.PortMapping{
			Name:          parts[0],
			HostPort:      hostPort,
			ContainerPort: containerPort,
			Protocol:      proto,
		})
	}
	return ports, nil
}

func cliConnectionAddress(connAddr *string, node *gamejanitor.GameserverNode, ports []gamejanitor.PortMapping) string {
	if connAddr != nil && *connAddr != "" {
		return *connAddr
	}
	if len(ports) > 0 {
		ip := ""
		if node != nil {
			if node.LanIP != "" {
				ip = node.LanIP
			} else if node.ExternalIP != "" {
				ip = node.ExternalIP
			}
		}
		if ip != "" {
			return fmt.Sprintf("%s:%d", ip, ports[0].HostPort)
		}
		return fmt.Sprintf("%d", ports[0].HostPort)
	}
	return ""
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
