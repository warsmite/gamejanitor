package backup

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/warsmite/gamejanitor/config"
)

// Storage abstracts backup file storage.
// LocalStorage for single-node (default), S3Storage for multi-node/power users.
type Storage interface {
	Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error
	Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error)
	Delete(ctx context.Context, gameserverID string, backupID string) error
	Size(ctx context.Context, gameserverID string, backupID string) (int64, error)

	// Archive operations — one archive per gameserver, keyed by gameserver ID only.
	SaveArchive(ctx context.Context, gameserverID string, reader io.Reader) error
	LoadArchive(ctx context.Context, gameserverID string) (io.ReadCloser, error)
	DeleteArchive(ctx context.Context, gameserverID string) error
}

// LocalStorage stores backups as files on local disk.
type LocalStorage struct {
	dataDir string
}

func NewLocalStorage(dataDir string) *LocalStorage {
	return &LocalStorage{dataDir: dataDir}
}

func (s *LocalStorage) backupPath(gameserverID, backupID string) string {
	return filepath.Join(s.dataDir, "backups", gameserverID, backupID+".tar.gz")
}

func (s *LocalStorage) Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error {
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

func (s *LocalStorage) Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error) {
	path := s.backupPath(gameserverID, backupID)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening backup file: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) Delete(ctx context.Context, gameserverID string, backupID string) error {
	path := s.backupPath(gameserverID, backupID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing backup file: %w", err)
	}
	return nil
}

func (s *LocalStorage) Size(ctx context.Context, gameserverID string, backupID string) (int64, error) {
	path := s.backupPath(gameserverID, backupID)
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat backup file: %w", err)
	}
	return fi.Size(), nil
}

func (s *LocalStorage) archivePath(gameserverID string) string {
	return filepath.Join(s.dataDir, "archives", gameserverID+".tar.gz")
}

func (s *LocalStorage) SaveArchive(ctx context.Context, gameserverID string, reader io.Reader) error {
	path := s.archivePath(gameserverID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating archive directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, reader); err != nil {
		os.Remove(path)
		return fmt.Errorf("writing archive data: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("closing archive file: %w", err)
	}
	return nil
}

func (s *LocalStorage) LoadArchive(ctx context.Context, gameserverID string) (io.ReadCloser, error) {
	f, err := os.Open(s.archivePath(gameserverID))
	if err != nil {
		return nil, fmt.Errorf("opening archive file: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) DeleteArchive(ctx context.Context, gameserverID string) error {
	if err := os.Remove(s.archivePath(gameserverID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing archive file: %w", err)
	}
	return nil
}

// List returns backup IDs under a given gameserver/prefix directory.
func (s *LocalStorage) List(ctx context.Context, prefix string) ([]string, error) {
	dir := filepath.Join(s.dataDir, "backups", prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Strip .tar.gz suffix to get the backup ID
		if ext := filepath.Ext(name); ext != "" {
			name = name[:len(name)-len(ext)]
			if ext2 := filepath.Ext(name); ext2 == ".tar" {
				name = name[:len(name)-len(ext2)]
			}
		}
		ids = append(ids, name)
	}
	return ids, nil
}

// S3Storage stores backups in an S3-compatible bucket.
type S3Storage struct {
	client *minio.Client
	bucket string
	log    *slog.Logger
}

func NewS3Storage(cfg *config.BackupStoreConfig, log *slog.Logger) (*S3Storage, error) {
	bucketLookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		bucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       cfg.UseSSL,
		Region:       cfg.Region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, fmt.Errorf("creating S3 client: %w", err)
	}

	exists, err := client.BucketExists(context.Background(), cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("checking S3 bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("S3 bucket %q does not exist", cfg.Bucket)
	}

	log.Info("backup store connected", "type", "s3", "bucket", cfg.Bucket, "endpoint", cfg.Endpoint)
	return &S3Storage{client: client, bucket: cfg.Bucket, log: log}, nil
}

func (s *S3Storage) objectKey(gameserverID, backupID string) string {
	return "backups/" + gameserverID + "/" + backupID + ".tar.gz"
}

func (s *S3Storage) Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error {
	key := s.objectKey(gameserverID, backupID)
	info, err := s.client.PutObject(ctx, s.bucket, key, reader, -1, minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		return fmt.Errorf("uploading backup to S3: %w", err)
	}
	s.log.Info("backup uploaded to S3", "key", key, "size", info.Size)
	return nil
}

func (s *S3Storage) Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error) {
	key := s.objectKey(gameserverID, backupID)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("downloading backup from S3: %w", err)
	}
	return obj, nil
}

func (s *S3Storage) Delete(ctx context.Context, gameserverID string, backupID string) error {
	key := s.objectKey(gameserverID, backupID)
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("deleting backup from S3: %w", err)
	}
	return nil
}

func (s *S3Storage) Size(ctx context.Context, gameserverID string, backupID string) (int64, error) {
	key := s.objectKey(gameserverID, backupID)
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("stat backup in S3: %w", err)
	}
	return info.Size, nil
}

func (s *S3Storage) archiveKey(gameserverID string) string {
	return "archives/" + gameserverID + ".tar.gz"
}

func (s *S3Storage) SaveArchive(ctx context.Context, gameserverID string, reader io.Reader) error {
	key := s.archiveKey(gameserverID)
	info, err := s.client.PutObject(ctx, s.bucket, key, reader, -1, minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		return fmt.Errorf("uploading archive to S3: %w", err)
	}
	s.log.Info("archive uploaded to S3", "key", key, "size", info.Size)
	return nil
}

func (s *S3Storage) LoadArchive(ctx context.Context, gameserverID string) (io.ReadCloser, error) {
	key := s.archiveKey(gameserverID)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("downloading archive from S3: %w", err)
	}
	return obj, nil
}

func (s *S3Storage) DeleteArchive(ctx context.Context, gameserverID string) error {
	key := s.archiveKey(gameserverID)
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("deleting archive from S3: %w", err)
	}
	return nil
}

// List returns backup IDs under a given prefix (gameserver ID or "db").
func (s *S3Storage) List(ctx context.Context, prefix string) ([]string, error) {
	objectPrefix := "backups/" + prefix + "/"
	var ids []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: objectPrefix}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		// Extract backup ID from key: "backups/{prefix}/{id}.tar.gz" → "{id}"
		name := obj.Key[len(objectPrefix):]
		if ext := filepath.Ext(name); ext != "" {
			name = name[:len(name)-len(ext)]
			if ext2 := filepath.Ext(name); ext2 == ".tar" {
				name = name[:len(name)-len(ext2)]
			}
		}
		if name != "" {
			ids = append(ids, name)
		}
	}
	return ids, nil
}
