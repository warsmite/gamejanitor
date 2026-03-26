package store

import (
	"database/sql"

	"github.com/warsmite/gamejanitor/model"
)

type ModStore struct {
	db *sql.DB
}

func NewModStore(db *sql.DB) *ModStore {
	return &ModStore{db: db}
}

func (s *ModStore) ListInstalledMods(gameserverID string) ([]model.InstalledMod, error) {
	rows, err := s.db.Query(
		"SELECT id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at FROM installed_mods WHERE gameserver_id = ? ORDER BY installed_at DESC",
		gameserverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mods []model.InstalledMod
	for rows.Next() {
		var m model.InstalledMod
		if err := rows.Scan(&m.ID, &m.GameserverID, &m.Source, &m.SourceID, &m.Name, &m.Version, &m.VersionID, &m.FilePath, &m.FileName, &m.Metadata, &m.InstalledAt); err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	if mods == nil {
		mods = []model.InstalledMod{}
	}
	return mods, rows.Err()
}

func (s *ModStore) GetInstalledMod(id string) (*model.InstalledMod, error) {
	var m model.InstalledMod
	err := s.db.QueryRow(
		"SELECT id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at FROM installed_mods WHERE id = ?",
		id,
	).Scan(&m.ID, &m.GameserverID, &m.Source, &m.SourceID, &m.Name, &m.Version, &m.VersionID, &m.FilePath, &m.FileName, &m.Metadata, &m.InstalledAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *ModStore) GetInstalledModBySource(gameserverID, source, sourceID string) (*model.InstalledMod, error) {
	var m model.InstalledMod
	err := s.db.QueryRow(
		"SELECT id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at FROM installed_mods WHERE gameserver_id = ? AND source = ? AND source_id = ?",
		gameserverID, source, sourceID,
	).Scan(&m.ID, &m.GameserverID, &m.Source, &m.SourceID, &m.Name, &m.Version, &m.VersionID, &m.FilePath, &m.FileName, &m.Metadata, &m.InstalledAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *ModStore) CreateInstalledMod(m *model.InstalledMod) error {
	_, err := s.db.Exec(
		"INSERT INTO installed_mods (id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.GameserverID, m.Source, m.SourceID, m.Name, m.Version, m.VersionID, m.FilePath, m.FileName, m.Metadata, m.InstalledAt,
	)
	return err
}

func (s *ModStore) DeleteInstalledMod(id string) error {
	_, err := s.db.Exec("DELETE FROM installed_mods WHERE id = ?", id)
	return err
}
