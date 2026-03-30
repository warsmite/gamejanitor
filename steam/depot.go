package steam

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// DepotDownloader orchestrates downloading a Steam depot to disk.
type DepotDownloader struct {
	client *Client
	cdn    *CDNClient
	log    *slog.Logger

	// Workers controls CDN download parallelism. Default is 8.
	Workers int
}

// NewDepotDownloader creates a downloader bound to an authenticated Steam client.
func NewDepotDownloader(client *Client, log *slog.Logger) *DepotDownloader {
	return &DepotDownloader{
		client:  client,
		log:     log.With("component", "steam_depot"),
		Workers: defaultCDNWorkers,
	}
}

// DownloadProgress reports download progress.
type DownloadProgress struct {
	TotalChunks     int
	CompletedChunks int
	TotalBytes      uint64
	CompletedBytes  uint64
}

// DownloadOptions controls a depot download operation.
type DownloadOptions struct {
	AppID   uint32
	DepotID uint32
	Branch  string // default "public"
	// DestDir is the directory to write files into.
	DestDir string
	// OldManifest, if set, enables delta updates (only download changed chunks).
	OldManifest *Manifest
	// OnProgress is called periodically with download progress. May be nil.
	OnProgress func(DownloadProgress)
}

// DownloadResult contains information about a completed download.
type DownloadResult struct {
	Manifest     *Manifest
	FilesWritten int
	FilesRemoved int
	BytesDownloaded uint64
	// IsDelta is true if this was a delta update from an old manifest.
	IsDelta bool
}

// Download fetches a depot and writes it to disk. If OldManifest is provided,
// only changed chunks are downloaded (delta update).
func (d *DepotDownloader) Download(ctx context.Context, opts DownloadOptions) (*DownloadResult, error) {
	if opts.Branch == "" {
		opts.Branch = "public"
	}

	// Resolve app info to get manifest ID
	d.log.Info("resolving app info",
		"app_id", opts.AppID,
		"depot_id", opts.DepotID,
		"branch", opts.Branch,
	)

	appInfo, err := d.client.GetAppInfo(ctx, opts.AppID, opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("get app info: %w", err)
	}

	// Find the requested depot
	var depotInfo *DepotInfo
	for i := range appInfo.Depots {
		if appInfo.Depots[i].DepotID == opts.DepotID {
			depotInfo = &appInfo.Depots[i]
			break
		}
	}
	// If no specific depot requested, use the first one
	if depotInfo == nil && opts.DepotID == 0 && len(appInfo.Depots) > 0 {
		depotInfo = &appInfo.Depots[0]
		d.log.Info("no depot specified, using first depot", "depot_id", depotInfo.DepotID)
	}
	if depotInfo == nil {
		return nil, fmt.Errorf("depot %d not found in app %d", opts.DepotID, opts.AppID)
	}

	d.log.Info("resolved depot",
		"depot_id", depotInfo.DepotID,
		"manifest_id", depotInfo.ManifestID,
		"build_id", appInfo.BuildID,
	)

	// Get depot decryption key
	depotKey, err := d.client.GetDepotDecryptionKey(ctx, depotInfo.DepotID, opts.AppID)
	if err != nil {
		return nil, fmt.Errorf("get depot key: %w", err)
	}

	// Get manifest request code
	requestCode, err := d.client.GetManifestRequestCode(ctx, opts.AppID, depotInfo.DepotID, depotInfo.ManifestID, opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("get manifest request code: %w", err)
	}

	// Initialize CDN client if needed
	if d.cdn == nil {
		hosts, err := d.client.GetCDNServers(ctx, 0)
		if err != nil {
			return nil, fmt.Errorf("get CDN servers: %w", err)
		}
		d.cdn = NewCDNClient(d.log, hosts)
	}

	// Download and parse manifest
	d.log.Info("downloading manifest", "manifest_id", depotInfo.ManifestID)
	manifest, err := DownloadManifest(ctx, d.cdn.hosts, depotInfo.DepotID, depotInfo.ManifestID, requestCode, depotKey)
	if err != nil {
		return nil, fmt.Errorf("download manifest: %w", err)
	}

	d.log.Info("manifest parsed",
		"files", len(manifest.Files),
		"depot_id", manifest.DepotID,
	)

	// Determine which chunks to download
	var chunksToDownload []ManifestChunk
	var filesToWrite []ManifestFile
	var filesToRemove []string
	isDelta := false

	if opts.OldManifest != nil {
		// Delta update
		diff := DiffManifests(opts.OldManifest, manifest)
		isDelta = true

		d.log.Info("delta update computed",
			"added", len(diff.FilesAdded),
			"changed", len(diff.FilesChanged),
			"removed", len(diff.FilesRemoved),
			"unchanged", diff.Unchanged,
			"chunks_to_download", len(diff.ChunksToDownload),
		)

		for _, c := range diff.ChunksToDownload {
			chunksToDownload = append(chunksToDownload, c)
		}
		filesToRemove = diff.FilesRemoved

		// Only write files that changed or were added
		changedSet := make(map[string]struct{})
		for _, name := range diff.FilesAdded {
			changedSet[name] = struct{}{}
		}
		for _, name := range diff.FilesChanged {
			changedSet[name] = struct{}{}
		}
		for _, f := range manifest.Files {
			if _, changed := changedSet[f.Filename]; changed {
				filesToWrite = append(filesToWrite, f)
			}
		}
	} else {
		// Full download — all chunks, all files
		filesToWrite = manifest.Files
		seen := make(map[string]struct{})
		for _, f := range manifest.Files {
			for _, c := range f.Chunks {
				cid := c.ChunkIDHex()
				if _, dup := seen[cid]; !dup {
					seen[cid] = struct{}{}
					chunksToDownload = append(chunksToDownload, c)
				}
			}
		}
	}

	// Create destination directory
	if err := os.MkdirAll(opts.DestDir, 0o755); err != nil {
		return nil, fmt.Errorf("create dest dir: %w", err)
	}

	// Download chunks into memory-mapped store keyed by chunk ID
	chunkStore := &chunkStore{data: make(map[string][]byte)}
	var totalBytes uint64
	for _, c := range chunksToDownload {
		totalBytes += uint64(c.CompressedSize)
	}

	progress := DownloadProgress{
		TotalChunks: len(chunksToDownload),
		TotalBytes:  totalBytes,
	}

	if len(chunksToDownload) > 0 {
		d.log.Info("downloading chunks",
			"count", len(chunksToDownload),
			"total_compressed_bytes", totalBytes,
		)

		err = d.cdn.DownloadChunksParallel(ctx, depotInfo.DepotID, depotKey, chunksToDownload, d.Workers,
			func(chunk ManifestChunk, data []byte) error {
				chunkStore.put(chunk.ChunkIDHex(), data)
				progress.CompletedChunks++
				progress.CompletedBytes += uint64(chunk.CompressedSize)
				if opts.OnProgress != nil {
					opts.OnProgress(progress)
				}
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("download chunks: %w", err)
		}
	}

	// Assemble files on disk
	filesWritten := 0
	for _, file := range filesToWrite {
		if err := d.assembleFile(opts.DestDir, &file, chunkStore); err != nil {
			return nil, fmt.Errorf("assemble %s: %w", file.Filename, err)
		}
		filesWritten++
	}

	// Remove deleted files (delta update)
	filesRemoved := 0
	for _, name := range filesToRemove {
		path := filepath.Join(opts.DestDir, filepath.FromSlash(name))
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			d.log.Warn("failed to remove deleted file", "path", path, "error", err)
		} else if err == nil {
			filesRemoved++
		}
	}

	d.log.Info("depot download complete",
		"files_written", filesWritten,
		"files_removed", filesRemoved,
		"is_delta", isDelta,
	)

	return &DownloadResult{
		Manifest:        manifest,
		FilesWritten:    filesWritten,
		FilesRemoved:    filesRemoved,
		BytesDownloaded: progress.CompletedBytes,
		IsDelta:         isDelta,
	}, nil
}

// assembleFile writes a single file by concatenating its chunks in order.
func (d *DepotDownloader) assembleFile(destDir string, file *ManifestFile, store *chunkStore) error {
	path := filepath.Join(destDir, filepath.FromSlash(file.Filename))

	// Directories are represented as files with flag 0x40 and size 0
	if file.Flags&0x40 != 0 {
		return os.MkdirAll(path, 0o755)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Steam manifests don't reliably carry executable permission info.
	// Use 0755 for all files — this runs inside a container where
	// restrictive permissions cause more problems than they solve.
	perm := os.FileMode(0o755)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, chunk := range file.Chunks {
		data, ok := store.get(chunk.ChunkIDHex())
		if !ok {
			return fmt.Errorf("missing chunk %s", chunk.ChunkIDHex())
		}
		if _, err := f.WriteAt(data, int64(chunk.Offset)); err != nil {
			return err
		}
	}

	return nil
}

// chunkStore is a thread-safe in-memory store for downloaded chunk data.
type chunkStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func (s *chunkStore) put(chunkID string, data []byte) {
	s.mu.Lock()
	s.data[chunkID] = data
	s.mu.Unlock()
}

func (s *chunkStore) get(chunkID string) ([]byte, bool) {
	s.mu.RLock()
	data, ok := s.data[chunkID]
	s.mu.RUnlock()
	return data, ok
}
