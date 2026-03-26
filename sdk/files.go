package gamejanitor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// FileService handles file management API calls for gameservers.
type FileService struct {
	client *Client
}

// List returns directory contents for a gameserver.
func (s *FileService) List(ctx context.Context, gameserverID, path string) ([]FileEntry, error) {
	u := "/api/gameservers/" + gameserverID + "/files"
	if path != "" {
		u += "?path=" + url.QueryEscape(path)
	}
	var entries []FileEntry
	if err := s.client.get(ctx, u, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Read returns the contents of a file.
func (s *FileService) Read(ctx context.Context, gameserverID, path string) (string, error) {
	u := "/api/gameservers/" + gameserverID + "/files/content?path=" + url.QueryEscape(path)
	var resp FileContent
	if err := s.client.get(ctx, u, &resp); err != nil {
		return "", err
	}
	return resp.Content, nil
}

// Write writes content to a file (max 10MB).
func (s *FileService) Write(ctx context.Context, gameserverID, path string, content []byte) error {
	u := s.client.baseURL + "/api/gameservers/" + gameserverID + "/files/content?path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("gamejanitor: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if s.client.tokenSource != nil {
		req.Header.Set("Authorization", "Bearer "+s.client.tokenSource.Token())
	}
	if s.client.userAgent != "" {
		req.Header.Set("User-Agent", s.client.userAgent)
	}
	return s.client.do(req, nil)
}

// Delete deletes a file or directory.
func (s *FileService) Delete(ctx context.Context, gameserverID, path string) error {
	u := "/api/gameservers/" + gameserverID + "/files?path=" + url.QueryEscape(path)
	return s.client.delete(ctx, u)
}

// Download returns a reader for the raw file content. Caller must close the reader.
func (s *FileService) Download(ctx context.Context, gameserverID, path string) (io.ReadCloser, error) {
	u := "/api/gameservers/" + gameserverID + "/files/download?path=" + url.QueryEscape(path)
	req, err := s.client.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.doRaw(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Upload uploads a file to the gameserver (max 100MB per file).
func (s *FileService) Upload(ctx context.Context, gameserverID, destPath, filename string, content io.Reader) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		return fmt.Errorf("gamejanitor: creating multipart form: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return fmt.Errorf("gamejanitor: writing file to multipart form: %w", err)
	}
	writer.Close()

	u := s.client.baseURL + "/api/gameservers/" + gameserverID + "/files/upload?path=" + url.QueryEscape(destPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return fmt.Errorf("gamejanitor: creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if s.client.tokenSource != nil {
		req.Header.Set("Authorization", "Bearer "+s.client.tokenSource.Token())
	}
	if s.client.userAgent != "" {
		req.Header.Set("User-Agent", s.client.userAgent)
	}
	return s.client.do(req, nil)
}

// Rename renames a file or directory.
func (s *FileService) Rename(ctx context.Context, gameserverID string, req *RenameFileRequest) error {
	return s.client.post(ctx, "/api/gameservers/"+gameserverID+"/files/rename", req, nil)
}

// Mkdir creates a directory.
func (s *FileService) Mkdir(ctx context.Context, gameserverID, path string) error {
	return s.client.post(ctx, "/api/gameservers/"+gameserverID+"/files/mkdir", &CreateDirectoryRequest{Path: path}, nil)
}
