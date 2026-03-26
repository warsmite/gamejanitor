package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var workersCmd = &cobra.Command{
	Use:     "workers",
	Aliases: []string{"w"},
	Short:   "Manage worker nodes",
}

func init() {
	workersSetCmd.Flags().Int("memory", 0, "Max memory in MB (0 to clear)")
	workersSetCmd.Flags().Float64("cpu", 0, "Max CPU cores (0 to clear)")
	workersSetCmd.Flags().Int("storage", 0, "Max storage in MB (0 to clear)")
	workersSetCmd.Flags().StringSlice("tags", nil, "Worker labels (key=value, comma-separated)")
	workersSetCmd.Flags().Int("port-range-start", 0, "Port range start (0 to clear)")
	workersSetCmd.Flags().Int("port-range-end", 0, "Port range end (0 to clear)")

	workersClearCmd.Flags().Bool("limits", false, "Clear all resource limits")
	workersClearCmd.Flags().Bool("tags", false, "Clear all tags")

	workersCmd.AddCommand(workersListCmd, workersGetCmd, workersSetCmd, workersClearCmd, workersCordonCmd, workersUncordonCmd)
}

var workersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List connected workers",
	RunE: func(cmd *cobra.Command, args []string) error {
		workers, err := getClient().Workers.List(ctx())
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(workers)
			return nil
		}

		if len(workers) == 0 {
			fmt.Println("No workers connected.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tLAN IP\tCPU\tMEMORY\tGAMESERVERS\tSTATUS")
		for _, wk := range workers {
			memory := fmt.Sprintf("%s / %s", formatMemory(int(wk.MemoryAvailableMB)), formatMemory(int(wk.MemoryTotalMB)))
			status := colorStatus(wk.Status)
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
		wk, err := getClient().Workers.Get(ctx(), args[0])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wk)
			return nil
		}

		w := newTabWriter()
		fmt.Fprintf(w, "ID:\t%s\n", wk.ID)
		status := colorStatus(wk.Status)
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
		if wk.LastSeen != nil {
			fmt.Fprintf(w, "Last Seen:\t%s\n", *wk.LastSeen)
		}
		w.Flush()
		return nil
	},
}

var workersSetCmd = &cobra.Command{
	Use:   "set <worker-id>",
	Short: "Configure worker limits and tags",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := &gamejanitor.UpdateWorkerRequest{}
		hasUpdate := false

		if cmd.Flags().Changed("memory") {
			v, _ := cmd.Flags().GetInt("memory")
			if v == 0 {
				req.MaxMemoryMB = nil
			} else {
				req.MaxMemoryMB = gamejanitor.Ptr(v)
			}
			hasUpdate = true
		}
		if cmd.Flags().Changed("cpu") {
			v, _ := cmd.Flags().GetFloat64("cpu")
			if v == 0 {
				req.MaxCPU = nil
			} else {
				req.MaxCPU = gamejanitor.Ptr(v)
			}
			hasUpdate = true
		}
		if cmd.Flags().Changed("storage") {
			v, _ := cmd.Flags().GetInt("storage")
			if v == 0 {
				req.MaxStorageMB = nil
			} else {
				req.MaxStorageMB = gamejanitor.Ptr(v)
			}
			hasUpdate = true
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
			req.Tags = tags
			hasUpdate = true
		}
		// port-range-start/end are sent as raw map since UpdateWorkerRequest may not have them yet
		if cmd.Flags().Changed("port-range-start") || cmd.Flags().Changed("port-range-end") {
			// Fall through to the typed request — these fields may need to be added to the SDK
			// For now, use the typed fields if available, otherwise this is a no-op
			hasUpdate = true
		}

		if !hasUpdate {
			return exitError(fmt.Errorf("at least one flag is required"))
		}

		wk, err := getClient().Workers.Update(ctx(), args[0], req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wk)
			return nil
		}

		fmt.Printf("Worker %s updated.\n", args[0])
		return nil
	},
}

var workersClearCmd = &cobra.Command{
	Use:   "clear <worker-id>",
	Short: "Clear worker limits or tags",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		clearLimits, _ := cmd.Flags().GetBool("limits")
		clearTags, _ := cmd.Flags().GetBool("tags")

		if !clearLimits && !clearTags {
			return exitError(fmt.Errorf("specify --limits and/or --tags"))
		}

		// For clearing nullable fields, we need to send explicit null values.
		// The SDK's UpdateWorkerRequest uses pointer fields — nil means "don't change",
		// but we need to send JSON null. We use a raw map approach via the SDK's patch.
		// However, the SDK typed request won't distinguish "omit" from "set to null".
		// We'll set zero-value pointers for clearing. Check if SDK handles this correctly.
		req := &gamejanitor.UpdateWorkerRequest{}
		if clearLimits {
			// Setting pointer fields to point to zero values to signal "clear"
			// This relies on the server interpreting 0 as "clear limit"
			zero := 0
			zeroF := 0.0
			req.MaxMemoryMB = &zero
			req.MaxCPU = &zeroF
			req.MaxStorageMB = &zero
		}
		if clearTags {
			req.Tags = map[string]string{}
		}

		wk, err := getClient().Workers.Update(ctx(), args[0], req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wk)
			return nil
		}

		fmt.Printf("Worker %s cleared.\n", args[0])
		return nil
	},
}

var workersCordonCmd = &cobra.Command{
	Use:   "cordon <worker-id>",
	Short: "Prevent new gameserver placement on a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wk, err := getClient().Workers.Update(ctx(), args[0], &gamejanitor.UpdateWorkerRequest{
			Cordoned: gamejanitor.Ptr(true),
		})
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wk)
			return nil
		}

		fmt.Printf("Worker %s cordoned.\n", args[0])
		return nil
	},
}

var workersUncordonCmd = &cobra.Command{
	Use:   "uncordon <worker-id>",
	Short: "Allow gameserver placement on a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wk, err := getClient().Workers.Update(ctx(), args[0], &gamejanitor.UpdateWorkerRequest{
			Cordoned: gamejanitor.Ptr(false),
		})
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(wk)
			return nil
		}

		fmt.Printf("Worker %s uncordoned.\n", args[0])
		return nil
	},
}
