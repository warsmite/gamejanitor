package model

import (
	"strings"

	"github.com/warsmite/gamejanitor/util/validate"
)

// ValidateCreate checks fields required for gameserver creation.
func (gs *Gameserver) ValidateCreate() error {
	var fe validate.FieldErrors
	fe.NotEmpty("name", gs.Name)
	fe.NotEmpty("game_id", gs.GameID)
	fe.MinInt("memory_limit_mb", gs.MemoryLimitMB, 0)
	fe.MinFloat("cpu_limit", gs.CPULimit, 0)
	fe.MinIntPtr("backup_limit", gs.BackupLimit, 0)
	fe.MinIntPtr("storage_limit_mb", gs.StorageLimitMB, 0)
	return fe.Err()
}

// ValidateUpdate checks fields for gameserver updates.
// Only validates fields that have non-zero values (indicating they were provided).
func (gs *Gameserver) ValidateUpdate() error {
	var fe validate.FieldErrors
	fe.MinInt("memory_limit_mb", gs.MemoryLimitMB, 0)
	fe.MinFloat("cpu_limit", gs.CPULimit, 0)
	fe.MinIntPtr("backup_limit", gs.BackupLimit, 0)
	fe.MinIntPtr("storage_limit_mb", gs.StorageLimitMB, 0)
	return fe.Err()
}

// ValidateCreate checks fields required for schedule creation.
func (s *Schedule) ValidateCreate() error {
	var fe validate.FieldErrors
	fe.NotEmpty("gameserver_id", s.GameserverID)
	fe.NotEmpty("type", s.Type)
	fe.OneOf("type", s.Type, []string{"restart", "backup", "command", "update"})
	fe.NotEmpty("cron_expr", s.CronExpr)
	return fe.Err()
}

// Validate checks webhook endpoint fields.
func (e *WebhookEndpoint) Validate() error {
	var fe validate.FieldErrors
	fe.NotEmpty("url", e.URL)
	if e.URL != "" && !strings.HasPrefix(e.URL, "http://") && !strings.HasPrefix(e.URL, "https://") {
		fe.Add("url", "must start with http:// or https://")
	}
	return fe.Err()
}

// Validate checks token fields.
func (t *Token) Validate() error {
	var fe validate.FieldErrors
	fe.NotEmpty("name", t.Name)
	return fe.Err()
}

// Validate checks pagination fields.
func (p *Pagination) Validate() error {
	var fe validate.FieldErrors
	fe.MinInt("limit", p.Limit, 0)
	fe.MinInt("offset", p.Offset, 0)
	return fe.Err()
}
