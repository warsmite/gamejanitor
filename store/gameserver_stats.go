package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

type GameserverStatsStore struct {
	db *sql.DB
}

func NewGameserverStatsStore(db *sql.DB) *GameserverStatsStore {
	return &GameserverStatsStore{db: db}
}

// InsertBatch writes a batch of raw stats samples in a single transaction.
func (s *GameserverStatsStore) InsertBatch(samples []model.StatsSample) error {
	if len(samples) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("starting stats batch transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO gameserver_stats
		(gameserver_id, resolution, timestamp, cpu_percent, memory_usage_mb, memory_limit_mb, volume_size_bytes, players_online)
		VALUES (?, 'raw', ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing stats insert: %w", err)
	}
	defer stmt.Close()

	for _, s := range samples {
		if _, err := stmt.Exec(
			s.GameserverID, s.Timestamp.UTC().Format(time.RFC3339),
			s.CPUPercent, s.MemoryUsageMB, s.MemoryLimitMB,
			s.VolumeSizeBytes, s.PlayersOnline,
		); err != nil {
			return fmt.Errorf("inserting stats sample: %w", err)
		}
	}

	return tx.Commit()
}

// QueryHistory returns stats samples for a gameserver at the appropriate resolution
// for the requested period.
func (s *GameserverStatsStore) QueryHistory(gameserverID string, period model.StatsPeriod) ([]model.StatsSample, error) {
	var resolution string
	var since string
	now := time.Now().UTC()

	switch period {
	case model.StatsPeriod1h:
		resolution = "raw"
		since = now.Add(-1 * time.Hour).Format(time.RFC3339)
	case model.StatsPeriod24h:
		resolution = "1m"
		since = now.Add(-24 * time.Hour).Format(time.RFC3339)
	case model.StatsPeriod7d:
		resolution = "5m"
		since = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	default:
		return nil, fmt.Errorf("invalid stats period: %s", period)
	}

	rows, err := s.db.Query(
		`SELECT gameserver_id, resolution, timestamp, cpu_percent, memory_usage_mb, memory_limit_mb, volume_size_bytes, players_online
		FROM gameserver_stats
		WHERE gameserver_id = ? AND resolution = ? AND timestamp >= ?
		ORDER BY timestamp`,
		gameserverID, resolution, since,
	)
	if err != nil {
		return nil, fmt.Errorf("querying stats history: %w", err)
	}
	defer rows.Close()

	var samples []model.StatsSample
	for rows.Next() {
		var s model.StatsSample
		if err := rows.Scan(&s.GameserverID, &s.Resolution, &s.Timestamp, &s.CPUPercent, &s.MemoryUsageMB, &s.MemoryLimitMB, &s.VolumeSizeBytes, &s.PlayersOnline); err != nil {
			return nil, fmt.Errorf("scanning stats row: %w", err)
		}
		samples = append(samples, s)
	}
	if samples == nil {
		samples = []model.StatsSample{}
	}
	return samples, rows.Err()
}

// Downsample aggregates samples from one resolution tier into the next.
// Rows older than `olderThan` in `fromRes` are grouped into `bucketSec`-second
// buckets, inserted as `toRes`, and the source rows are deleted.
func (s *GameserverStatsStore) Downsample(fromRes, toRes string, olderThan time.Duration, bucketSec int) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("starting downsample transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert aggregated rows
	_, err = tx.Exec(`
		INSERT INTO gameserver_stats
			(gameserver_id, resolution, timestamp, cpu_percent, memory_usage_mb, memory_limit_mb, volume_size_bytes, players_online)
		SELECT
			gameserver_id,
			?,
			datetime((CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ?, 'unixepoch'),
			AVG(cpu_percent),
			AVG(memory_usage_mb),
			MAX(memory_limit_mb),
			MAX(volume_size_bytes),
			MAX(players_online)
		FROM gameserver_stats
		WHERE resolution = ? AND timestamp < ? AND strftime('%s', timestamp) IS NOT NULL
		GROUP BY gameserver_id, CAST(strftime('%s', timestamp) AS INTEGER) / ?`,
		toRes, bucketSec, bucketSec, fromRes, cutoff, bucketSec,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting downsampled stats: %w", err)
	}

	// Delete source rows
	result, err := tx.Exec(
		`DELETE FROM gameserver_stats WHERE resolution = ? AND timestamp < ?`,
		fromRes, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting downsampled source rows: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing downsample: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return int(deleted), nil
}

// Prune deletes stats rows older than the given duration at a specific resolution.
func (s *GameserverStatsStore) Prune(resolution string, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.Exec(
		`DELETE FROM gameserver_stats WHERE resolution = ? AND timestamp < ?`,
		resolution, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning stats: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}
