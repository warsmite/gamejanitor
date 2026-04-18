package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/worker"
)

// --- Worker interface: File operations (delegate to shared helpers) ---

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	return ListFilesDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	return ReadFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	return OpenFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	return WriteFileDirect(w.resolve, ctx, volumeName, path, content, perm)
}
func (w *LocalWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	return WriteFileStreamDirect(w.resolve, ctx, volumeName, path, reader, perm)
}
func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	return DeletePathDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	return CreateDirectoryDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	return RenamePathDirect(w.resolve, ctx, volumeName, from, to)
}
func (w *LocalWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	return DownloadFileDirect(w.resolve, ctx, volumeName, url, destPath, expectedHash, maxBytes)
}

// --- Worker interface: Copy operations ---

func (w *LocalWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
	// Instance filesystem is the volume — read directly
	dir := w.instanceDir(instanceID)
	manifestData, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest instanceManifest
	json.Unmarshal(manifestData, &manifest)
	if manifest.VolumeName == "" {
		return nil, fmt.Errorf("instance %s has no volume", instanceID)
	}
	mountpoint, err := w.resolve(ctx, manifest.VolumeName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(mountpoint, path))
}

func (w *LocalWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
	dir := w.instanceDir(instanceID)
	manifestData, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest instanceManifest
	json.Unmarshal(manifestData, &manifest)
	if manifest.VolumeName == "" {
		return fmt.Errorf("instance %s has no volume", instanceID)
	}
	mountpoint, err := w.resolve(ctx, manifest.VolumeName)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(mountpoint, path)
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	return os.WriteFile(fullPath, content, 0644)
}

func (w *LocalWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("CopyDirFromInstance not supported in local worker")
}

func (w *LocalWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	return fmt.Errorf("CopyTarToInstance not supported in local worker")
}
