package constants

// Pagination
const (
	PaginationMaxLimit        = 200
	PaginationDefaultLimit    = 50
	PaginationDefaultLogTail  = 100
	PaginationDefaultModLimit = 20
)

// File size limits
const (
	MaxFileWriteBytes    = 10 * 1024 * 1024  // 10 MB — inline file writes via API
	MaxFileUploadBytes   = 100 * 1024 * 1024 // 100 MB — multipart file uploads
	MaxModDownloadBytes  = 100 * 1024 * 1024 // 100 MB — mod downloads (Modrinth, Workshop, generic)
	MaxUmodDownloadBytes = 50 * 1024 * 1024  // 50 MB — uMod plugin downloads
)

// Container user identity — game processes run as this UID/GID inside containers.
const (
	GameserverUID  = 1001
	GameserverGID  = 1001
	GameserverPerm = 0644
)
