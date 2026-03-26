package model

import (
	"database/sql"
	"encoding/json"
	"time"
)

type InstalledMod struct {
	ID           string          `json:"id"`
	GameserverID string          `json:"gameserver_id"`
	Source       string          `json:"source"`
	SourceID     string          `json:"source_id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	VersionID    string          `json:"version_id"`
	FilePath     string          `json:"file_path"`
	FileName     string          `json:"file_name"`
	Metadata     json.RawMessage `json:"metadata"`
	InstalledAt  time.Time       `json:"installed_at"`
}

func ListInstalledMods(db *sql.DB, gameserverID string) ([]InstalledMod, error) {
	rows, err := db.Query(
		"SELECT id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at FROM installed_mods WHERE gameserver_id = ? ORDER BY installed_at DESC",
		gameserverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mods []InstalledMod
	for rows.Next() {
		var m InstalledMod
		if err := rows.Scan(&m.ID, &m.GameserverID, &m.Source, &m.SourceID, &m.Name, &m.Version, &m.VersionID, &m.FilePath, &m.FileName, &m.Metadata, &m.InstalledAt); err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	if mods == nil {
		mods = []InstalledMod{}
	}
	return mods, rows.Err()
}

func GetInstalledMod(db *sql.DB, id string) (*InstalledMod, error) {
	var m InstalledMod
	err := db.QueryRow(
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

func GetInstalledModBySource(db *sql.DB, gameserverID, source, sourceID string) (*InstalledMod, error) {
	var m InstalledMod
	err := db.QueryRow(
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

func CreateInstalledMod(db *sql.DB, m *InstalledMod) error {
	_, err := db.Exec(
		"INSERT INTO installed_mods (id, gameserver_id, source, source_id, name, version, version_id, file_path, file_name, metadata, installed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.GameserverID, m.Source, m.SourceID, m.Name, m.Version, m.VersionID, m.FilePath, m.FileName, m.Metadata, m.InstalledAt,
	)
	return err
}

func DeleteInstalledMod(db *sql.DB, id string) error {
	_, err := db.Exec("DELETE FROM installed_mods WHERE id = ?", id)
	return err
}
