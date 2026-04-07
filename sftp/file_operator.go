package sftp

import (
	"context"
	"fmt"
	"os"

	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/worker"
)

// WorkerFileOperator adapts a worker.Worker to the FileOperator interface.
// Used on worker nodes where file operations are local.
type WorkerFileOperator struct {
	w worker.Worker
}

func NewWorkerFileOperator(w worker.Worker) *WorkerFileOperator {
	return &WorkerFileOperator{w: w}
}

// DispatcherFileOperator routes file operations through the dispatcher to the correct worker.
// Used on controller/standalone nodes where SFTP needs to reach remote workers.
type DispatcherFileOperator struct {
	dispatcher *cluster.Dispatcher
	// gameserverID is used by the dispatcher to route to the correct worker.
	// Set per-session via the auth callback.
	gameserverID string
}

func NewDispatcherFileOperator(dispatcher *cluster.Dispatcher, gameserverID string) *DispatcherFileOperator {
	return &DispatcherFileOperator{dispatcher: dispatcher, gameserverID: gameserverID}
}

func (o *DispatcherFileOperator) ListFiles(volumeName string, path string) ([]FileEntry, error) {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return nil, fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	entries, err := w.ListFiles(context.Background(), volumeName, path)
	if err != nil {
		return nil, err
	}
	result := make([]FileEntry, len(entries))
	for i, e := range entries {
		result[i] = FileEntry{
			Name:    e.Name,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime.Unix(),
		}
	}
	return result, nil
}

func (o *DispatcherFileOperator) ReadFile(volumeName string, path string) ([]byte, error) {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return nil, fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	return w.ReadFile(context.Background(), volumeName, path)
}

func (o *DispatcherFileOperator) WriteFile(volumeName string, path string, content []byte, perm os.FileMode) error {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	return w.WriteFile(context.Background(), volumeName, path, content, perm)
}

func (o *DispatcherFileOperator) DeletePath(volumeName string, path string) error {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	return w.DeletePath(context.Background(), volumeName, path)
}

func (o *DispatcherFileOperator) CreateDirectory(volumeName string, path string) error {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	return w.CreateDirectory(context.Background(), volumeName, path)
}

func (o *DispatcherFileOperator) RenamePath(volumeName string, from string, to string) error {
	w := o.dispatcher.WorkerFor(o.gameserverID)
	if w == nil {
		return fmt.Errorf("worker unavailable for gameserver %s", o.gameserverID)
	}
	return w.RenamePath(context.Background(), volumeName, from, to)
}

func (o *WorkerFileOperator) ListFiles(volumeName string, path string) ([]FileEntry, error) {
	entries, err := o.w.ListFiles(context.Background(), volumeName, path)
	if err != nil {
		return nil, err
	}
	result := make([]FileEntry, len(entries))
	for i, e := range entries {
		result[i] = FileEntry{
			Name:    e.Name,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime.Unix(),
		}
	}
	return result, nil
}

func (o *WorkerFileOperator) ReadFile(volumeName string, path string) ([]byte, error) {
	return o.w.ReadFile(context.Background(), volumeName, path)
}

func (o *WorkerFileOperator) WriteFile(volumeName string, path string, content []byte, perm os.FileMode) error {
	return o.w.WriteFile(context.Background(), volumeName, path, content, perm)
}

func (o *WorkerFileOperator) DeletePath(volumeName string, path string) error {
	return o.w.DeletePath(context.Background(), volumeName, path)
}

func (o *WorkerFileOperator) CreateDirectory(volumeName string, path string) error {
	return o.w.CreateDirectory(context.Background(), volumeName, path)
}

func (o *WorkerFileOperator) RenamePath(volumeName string, from string, to string) error {
	return o.w.RenamePath(context.Background(), volumeName, from, to)
}
