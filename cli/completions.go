package cli

import (
	"github.com/spf13/cobra"
)

// completeGameserverName provides dynamic completion for gameserver names/IDs.
func completeGameserverName(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := fetchGameserverList()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeGameserverNameAtPos allows completion at a specific arg position.
func completeGameserverNameAtPos(pos int) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != pos {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries, err := fetchGameserverList()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeGameID provides dynamic completion for game IDs.
func completeGameID(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	games, err := getClient().Games.List(ctx())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for _, g := range games {
		ids = append(ids, g.ID)
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeWorkerID provides dynamic completion for worker IDs.
func completeWorkerID(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	workers, err := getClient().Workers.List(ctx())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for _, w := range workers {
		ids = append(ids, w.ID)
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeClusterName provides completion for cluster context names.
func completeClusterName(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadClustersConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for name := range cfg.Clusters {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func registerCompletions() {
	// Top-level gameserver commands
	for _, cmd := range []*cobra.Command{
		getCmd, editCmd, deleteCmd, startCmd, stopCmd, restartCmd, logsCmd, commandCmd,
		updateGameCmd, reinstallCmd, migrateCmd,
	} {
		cmd.ValidArgsFunction = completeGameserverName
	}
	// status takes optional gameserver arg
	statusCmd.ValidArgsFunction = completeGameserverName

	// create takes <name> <game> — complete game ID for second arg
	createCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 1 {
			return completeGameID(cmd, nil, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Backups/schedules take gameserver as first arg
	for _, cmd := range backupsCmd.Commands() {
		cmd.ValidArgsFunction = completeGameserverNameAtPos(0)
	}
	for _, cmd := range schedulesCmd.Commands() {
		cmd.ValidArgsFunction = completeGameserverNameAtPos(0)
	}

	// Games
	gamesGetCmd.ValidArgsFunction = completeGameID

	// Workers
	workersGetCmd.ValidArgsFunction = completeWorkerID
	workersSetCmd.ValidArgsFunction = completeWorkerID
	workersClearCmd.ValidArgsFunction = completeWorkerID
	workersCordonCmd.ValidArgsFunction = completeWorkerID
	workersUncordonCmd.ValidArgsFunction = completeWorkerID

	// Cluster
	clusterUseCmd.ValidArgsFunction = completeClusterName
	clusterRemoveCmd.ValidArgsFunction = completeClusterName
}
