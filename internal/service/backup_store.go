package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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

// S3Store stores backups in an S3-compatible bucket.
type S3Store struct {
	client *minio.Client
	bucket string
	log    *slog.Logger
}

func NewS3Store(endpoint, bucket, region, accessKey, secretKey string, pathStyle, useSSL bool, log *slog.Logger) (*S3Store, error) {
	bucketLookup := minio.BucketLookupAuto
	if pathStyle {
		bucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:       useSSL,
		Region:       region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, fmt.Errorf("creating S3 client: %w", err)
	}

	exists, err := client.BucketExists(context.Background(), bucket)
	if err != nil {
		return nil, fmt.Errorf("checking S3 bucket %q: %w", bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("S3 bucket %q does not exist", bucket)
	}

	log.Info("S3 backup store connected", "bucket", bucket, "endpoint", endpoint)
	return &S3Store{client: client, bucket: bucket, log: log}, nil
}

func (s *S3Store) objectKey(gameserverID, backupID string) string {
	return "backups/" + gameserverID + "/" + backupID + ".tar.gz"
}

func (s *S3Store) Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error {
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

func (s *S3Store) Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error) {
	key := s.objectKey(gameserverID, backupID)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("downloading backup from S3: %w", err)
	}
	return obj, nil
}

func (s *S3Store) Delete(ctx context.Context, gameserverID string, backupID string) error {
	key := s.objectKey(gameserverID, backupID)
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("deleting backup from S3: %w", err)
	}
	return nil
}

func (s *S3Store) Size(ctx context.Context, gameserverID string, backupID string) (int64, error) {
	key := s.objectKey(gameserverID, backupID)
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("stat backup in S3: %w", err)
	}
	return info.Size, nil
}
