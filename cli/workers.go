package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var workersCmd = &cobra.Command{
	Use:     "workers",
	Aliases: []string{"w"},
	Short:   "Manage remote workers",
}

var workersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List connected workers",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/workers")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var workers []struct {
			ID                string   `json:"id"`
			LanIP             string   `json:"lan_ip"`
			CPUCores          int64    `json:"cpu_cores"`
			MemoryTotalMB     int64    `json:"memory_total_mb"`
			MemoryAvailableMB int64    `json:"memory_available_mb"`
			GameserverCount   int      `json:"gameserver_count"`
			AllocatedMemoryMB int      `json:"allocated_memory_mb"`
			AllocatedCPU      float64  `json:"allocated_cpu"`
			MaxMemoryMB       *int     `json:"max_memory_mb"`
			MaxCPU            *float64 `json:"max_cpu"`
			MaxStorageMB      *int     `json:"max_storage_mb"`
			Cordoned          bool     `json:"cordoned"`
			Status            string   `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &workers); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if len(workers) == 0 {
			fmt.Println("No workers connected.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tLAN IP\tCPU\tMEMORY\tGAMESERVERS\tSTATUS")
		for _, wk := range workers {
			memory := fmt.Sprintf("%s / %s", formatMemory(int(wk.MemoryAvailableMB)), formatMemory(int(wk.MemoryTotalMB)))
			status := wk.Status
			if wk.Cordoned {
				status += " (cordoned)"
			}

			fmt.Fprintf(w, "%s\t%s\t%d cores\t%s\t%d\t%s\n",
				wk.ID, wk.LanIP, wk.CPUCores, memory, wk.GameserverCount, status)
		}
		w.Flush()
		return nil
	},
}

var workersGetCmd = &cobra.Command{
	Use:   "get <worker-id>",
	Short: "Get details for a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/workers/" + args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var wk struct {
			ID                string   `json:"id"`
			LanIP             string   `json:"lan_ip"`
			ExternalIP        string   `json:"external_ip"`
			CPUCores          int64    `json:"cpu_cores"`
			MemoryTotalMB     int64    `json:"memory_total_mb"`
			MemoryAvailableMB int64    `json:"memory_available_mb"`
			GameserverCount   int      `json:"gameserver_count"`
			AllocatedMemoryMB int      `json:"allocated_memory_mb"`
			AllocatedCPU      float64  `json:"allocated_cpu"`
			MaxMemoryMB       *int     `json:"max_memory_mb"`
			MaxCPU            *float64 `json:"max_cpu"`
			MaxStorageMB      *int     `json:"max_storage_mb"`
			Cordoned          bool     `json:"cordoned"`
			Status            string   `json:"status"`
			LastSeen          string   `json:"last_seen"`
		}
		if err := json.Unmarshal(resp.Data, &wk); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		w := newTabWriter()
		fmt.Fprintf(w, "ID:\t%s\n", wk.ID)
		status := wk.Status
		if wk.Cordoned {
			status += " (cordoned)"
		}
		fmt.Fprintf(w, "Status:\t%s\n", status)
		fmt.Fprintf(w, "LAN IP:\t%s\n", wk.LanIP)
		if wk.ExternalIP != "" {
			fmt.Fprintf(w, "External IP:\t%s\n", wk.ExternalIP)
		}
		fmt.Fprintf(w, "CPU:\t%d cores\n", wk.CPUCores)
		fmt.Fprintf(w, "Memory:\t%s / %s available\n", formatMemory(int(wk.MemoryAvailableMB)), formatMemory(int(wk.MemoryTotalMB)))
		fmt.Fprintf(w, "Gameservers:\t%d\n", wk.GameserverCount)
		fmt.Fprintf(w, "Allocated Memory:\t%s\n", formatMemory(wk.AllocatedMemoryMB))
		fmt.Fprintf(w, "Allocated CPU:\t%.1f\n", wk.AllocatedCPU)

		if wk.MaxMemoryMB != nil {
			fmt.Fprintf(w, "Max Memory:\t%s\n", formatMemory(*wk.MaxMemoryMB))
		}
		if wk.MaxCPU != nil {
			fmt.Fprintf(w, "Max CPU:\t%.1f\n", *wk.MaxCPU)
		}
		if wk.MaxStorageMB != nil {
			fmt.Fprintf(w, "Max Storage:\t%s\n", formatMemory(*wk.MaxStorageMB))
		}
		fmt.Fprintf(w, "Last Seen:\t%s\n", wk.LastSeen)
		w.Flush()
		return nil
	},
}

var workersUpdateCmd = &cobra.Command{
	Use:   "update <worker-id>",
	Short: "Update worker settings (limits, cordon, tags)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := make(map[string]any)

		if cmd.Flags().Changed("max-memory") {
			v, _ := cmd.Flags().GetInt("max-memory")
			if v == 0 {
				body["max_memory_mb"] = nil
			} else {
				body["max_memory_mb"] = v
			}
		}
		if cmd.Flags().Changed("max-cpu") {
			v, _ := cmd.Flags().GetFloat64("max-cpu")
			if v == 0 {
				body["max_cpu"] = nil
			} else {
				body["max_cpu"] = v
			}
		}
		if cmd.Flags().Changed("max-storage") {
			v, _ := cmd.Flags().GetInt("max-storage")
			if v == 0 {
				body["max_storage_mb"] = nil
			} else {
				body["max_storage_mb"] = v
			}
		}
		if cmd.Flags().Changed("cordoned") {
			v, _ := cmd.Flags().GetBool("cordoned")
			body["cordoned"] = v
		}
		if cmd.Flags().Changed("tags") {
			v, _ := cmd.Flags().GetStringSlice("tags")
			tags := make(map[string]string, len(v))
			for _, entry := range v {
				parts := strings.SplitN(entry, "=", 2)
				if len(parts) != 2 {
					return exitError(fmt.Errorf("invalid tag %q: must be key=value", entry))
				}
				tags[parts[0]] = parts[1]
			}
			body["tags"] = tags
		}

		if len(body) == 0 {
			return exitError(fmt.Errorf("at least one flag is required"))
		}

		resp, err := apiPatch("/api/workers/"+args[0], body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Worker %s updated.\n", args[0])
		return nil
	},
}

func init() {
	workersUpdateCmd.Flags().Int("max-memory", 0, "Max memory in MB (0 to clear)")
	workersUpdateCmd.Flags().Float64("max-cpu", 0, "Max CPU cores (0 to clear)")
	workersUpdateCmd.Flags().Int("max-storage", 0, "Max storage in MB (0 to clear)")
	workersUpdateCmd.Flags().Bool("cordoned", false, "Cordon (true) or uncordon (false) the worker")
	workersUpdateCmd.Flags().StringSlice("tags", nil, "Set worker labels (key=value, comma-separated)")

	workersCmd.AddCommand(workersListCmd, workersGetCmd, workersUpdateCmd)
}
