package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BackupStore abstracts backup file storage.
// LocalStore for single-node (default), S3Store for multi-node/power users.
type BackupStore interface {
	Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error
	Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error)
	Delete(ctx context.Context, gameserverID string, backupID string) error
	Size(ctx context.Context, gameserverID string, backupID string) (int64, error)
}

// LocalStore stores backups as files on local disk.
type LocalStore struct {
	dataDir string
}

func NewLocalStore(dataDir string) *LocalStore {
	return &LocalStore{dataDir: dataDir}
}

func (s *LocalStore) backupPath(gameserverID, backupID string) string {
	return filepath.Join(s.dataDir, "backups", gameserverID, backupID+".tar.gz")
}

func (s *LocalStore) Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error {
	path := s.backupPath(gameserverID, backupID)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating backup directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating backup file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		os.Remove(path)
		return fmt.Errorf("writing backup data: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("closing backup file: %w", err)
	}

	return nil
}

func (s *LocalStore) Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error) {
	path := s.backupPath(gameserverID, backupID)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening backup file: %w", err)
	}
	return f, nil
}

func (s *LocalStore) Delete(ctx context.Context, gameserverID string, backupID string) error {
	path := s.backupPath(gameserverID, backupID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing backup file: %w", err)
	}
	return nil
}

func (s *LocalStore) Size(ctx context.Context, gameserverID string, backupID string) (int64, error) {
	path := s.backupPath(gameserverID, backupID)
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat backup file: %w", err)
	}
	return fi.Size(), nil
}
