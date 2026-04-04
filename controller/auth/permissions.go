package auth

const (
	// Gameserver lifecycle
	PermGameserverCreate     = "gameserver.create"
	PermGameserverDelete     = "gameserver.delete"
	PermGameserverStart      = "gameserver.start"
	PermGameserverStop       = "gameserver.stop"
	PermGameserverRestart    = "gameserver.restart"
	PermGameserverUpdateGame = "gameserver.update-game"
	PermGameserverReinstall  = "gameserver.reinstall"
	PermGameserverArchive    = "gameserver.archive"
	PermGameserverUnarchive  = "gameserver.unarchive"

	// Gameserver configuration (per-field)
	PermGameserverConfigureName        = "gameserver.configure.name"
	PermGameserverConfigureEnv         = "gameserver.configure.env"
	PermGameserverConfigureResources   = "gameserver.configure.resources"
	PermGameserverConfigurePorts       = "gameserver.configure.ports"
	PermGameserverConfigureConnection  = "gameserver.configure.connection"
	PermGameserverConfigureAutoRestart = "gameserver.configure.auto-restart"
	PermGameserverRegenerateSFTP       = "gameserver.regenerate-sftp"

	// Gameserver access
	PermGameserverFilesRead  = "gameserver.files.read"
	PermGameserverFilesWrite = "gameserver.files.write"
	PermGameserverLogs       = "gameserver.logs"
	PermGameserverCommand    = "gameserver.command"

	// Mods
	PermGameserverModsRead  = "gameserver.mods.read"
	PermGameserverModsWrite = "gameserver.mods.write"

	// Backups
	PermBackupRead     = "backup.read"
	PermBackupCreate   = "backup.create"
	PermBackupDelete   = "backup.delete"
	PermBackupRestore  = "backup.restore"
	PermBackupDownload = "backup.download"

	// Schedules
	PermScheduleRead   = "schedule.read"
	PermScheduleCreate = "schedule.create"
	PermScheduleUpdate = "schedule.update"
	PermScheduleDelete = "schedule.delete"

	// Worker
	PermWorkerConnect = "worker.connect"
)

// AllPermissions is every permission that can be assigned to a user token.
// These are all gameserver-scoped (except gameserver.create which is cluster-level).
// Cluster management (settings, tokens, nodes, webhooks) requires admin role.
var AllPermissions = []string{
	PermGameserverCreate, PermGameserverDelete,
	PermGameserverStart, PermGameserverStop, PermGameserverRestart,
	PermGameserverUpdateGame, PermGameserverReinstall,
	PermGameserverArchive, PermGameserverUnarchive,
	PermGameserverConfigureName, PermGameserverConfigureEnv,
	PermGameserverConfigureResources, PermGameserverConfigurePorts,
	PermGameserverConfigureConnection, PermGameserverConfigureAutoRestart,
	PermGameserverRegenerateSFTP,
	PermGameserverFilesRead, PermGameserverFilesWrite,
	PermGameserverLogs, PermGameserverCommand,
	PermGameserverModsRead, PermGameserverModsWrite,
	PermBackupRead, PermBackupCreate, PermBackupDelete, PermBackupRestore, PermBackupDownload,
	PermScheduleRead, PermScheduleCreate, PermScheduleUpdate, PermScheduleDelete,
}

func isValidPermission(p string) bool {
	for _, valid := range AllPermissions {
		if p == valid {
			return true
		}
	}
	return p == PermWorkerConnect
}
