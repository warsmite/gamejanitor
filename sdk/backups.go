package gamejanitor

import (
	"context"
	"io"
	"net/http"
)

// BackupService handles backup-related API calls.
type BackupService struct {
	client *Client
}

// List returns backups for a gameserver.
func (s *BackupService) List(ctx context.Context, gameserverID string, opts *ListOptions) ([]Backup, error) {
	path := "/api/gameservers/" + gameserverID + "/backups" + opts.encode()
	var backups []Backup
	if err := s.client.get(ctx, path, &backups); err != nil {
		return nil, err
	}
	return backups, nil
}

// Create triggers a backup for a gameserver and returns the pending backup
// record. The actual backup completes asynchronously — poll List or subscribe
// to backup.completed / backup.failed events to observe completion.
func (s *BackupService) Create(ctx context.Context, gameserverID string, req *CreateBackupRequest) (*Backup, error) {
	var backup Backup
	if err := s.client.post(ctx, "/api/gameservers/"+gameserverID+"/backups", req, &backup); err != nil {
		return nil, err
	}
	return &backup, nil
}

// Download returns a reader for the backup archive (tar.gz). Caller must close the reader.
func (s *BackupService) Download(ctx context.Context, gameserverID, backupID string) (io.ReadCloser, error) {
	path := "/api/gameservers/" + gameserverID + "/backups/" + backupID + "/download"
	req, err := s.client.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.doRaw(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Restore triggers a backup restore. Returns immediately (202 Accepted).
func (s *BackupService) Restore(ctx context.Context, gameserverID, backupID string) error {
	return s.client.post(ctx, "/api/gameservers/"+gameserverID+"/backups/"+backupID+"/restore", nil, nil)
}

// Delete deletes a backup.
func (s *BackupService) Delete(ctx context.Context, gameserverID, backupID string) error {
	return s.client.delete(ctx, "/api/gameservers/"+gameserverID+"/backups/"+backupID)
}
