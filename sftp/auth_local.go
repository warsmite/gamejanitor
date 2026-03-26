package sftp

import (
	"database/sql"
	"fmt"

	"github.com/warsmite/gamejanitor/model"
	"golang.org/x/crypto/bcrypt"
)

// LocalAuth validates SFTP credentials directly against the database.
// Used on standalone and controller nodes.
type LocalAuth struct {
	db *sql.DB
}

func NewLocalAuth(db *sql.DB) *LocalAuth {
	return &LocalAuth{db: db}
}

func (a *LocalAuth) ValidateLogin(username, password string) (string, string, error) {
	gs, err := model.GetGameserverBySFTPUsername(a.db, username)
	if err != nil || gs == nil {
		return "", "", fmt.Errorf("unknown sftp user %s", username)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(gs.HashedSFTPPassword), []byte(password)); err != nil {
		return "", "", fmt.Errorf("invalid credentials")
	}
	return gs.ID, gs.VolumeName, nil
}
