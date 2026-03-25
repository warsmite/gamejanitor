package service

const (
	// Gameserver lifecycle
	PermGameserverCreate  = "gameserver.create"
	PermGameserverUpdate  = "gameserver.update"
	PermGameserverDelete  = "gameserver.delete"
	PermGameserverStart   = "gameserver.start"
	PermGameserverStop    = "gameserver.stop"
	PermGameserverRestart = "gameserver.restart"

	// Gameserver access
	PermGameserverFilesRead  = "gameserver.files.read"
	PermGameserverFilesWrite = "gameserver.files.write"
	PermGameserverLogs       = "gameserver.logs"
	PermGameserverCommand    = "gameserver.command"

	// Gameserver config
	PermGameserverEditName = "gameserver.edit.name"
	PermGameserverEditEnv  = "gameserver.edit.env"

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

	// Cluster management
	PermSettingsView   = "settings.view"
	PermSettingsEdit   = "settings.edit"
	PermTokensManage   = "tokens.manage"
	PermNodesManage    = "nodes.manage"
	PermWebhooksManage = "webhooks.manage"

	// Worker
	PermWorkerConnect = "worker.connect"
)

// AllPermissions is every permission that can be assigned to a token.
// Admin tokens are created with all of these.
var AllPermissions = []string{
	PermGameserverCreate, PermGameserverUpdate, PermGameserverDelete,
	PermGameserverStart, PermGameserverStop, PermGameserverRestart,
	PermGameserverFilesRead, PermGameserverFilesWrite,
	PermGameserverLogs, PermGameserverCommand,
	PermGameserverEditName, PermGameserverEditEnv,
	PermGameserverModsRead, PermGameserverModsWrite,
	PermBackupRead, PermBackupCreate, PermBackupDelete, PermBackupRestore, PermBackupDownload,
	PermScheduleRead, PermScheduleCreate, PermScheduleUpdate, PermScheduleDelete,
	PermSettingsView, PermSettingsEdit,
	PermTokensManage, PermNodesManage, PermWebhooksManage,
}

func isValidPermission(p string) bool {
	for _, valid := range AllPermissions {
		if p == valid {
			return true
		}
	}
	// Worker connect is valid but not in AllPermissions (not granted to admin tokens)
	return p == PermWorkerConnect
}
