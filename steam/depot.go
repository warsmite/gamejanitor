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
	Manifest        *Manifest
	FilesWritten    int
	FilesRemoved    int
	BytesDownloaded uint64
	// IsDelta is true if this was a delta update from an old manifest.
	IsDelta bool
}

// chunkTarget describes where a chunk should be written on disk.
type chunkTarget struct {
	path   string
	offset uint64
}

// Download fetches a depot and writes it to disk. If OldManifest is provided,
// only changed chunks are downloaded (delta update).
// Chunks are written directly to their target files as they download — no buffering in memory.
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

	// Determine which chunks to download and which files to write
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

	// Create directories and pre-create files before downloading chunks
	for _, file := range filesToWrite {
		path := filepath.Join(opts.DestDir, filepath.FromSlash(file.Filename))
		if file.Flags&0x40 != 0 {
			os.MkdirAll(path, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create dir for %s: %w", file.Filename, err)
		}
	}

	// Build chunk-to-file index: for each chunk ID, where does it go on disk?
	// A chunk may appear in multiple files (rare but possible with hardlinked content).
	chunkTargets := make(map[string][]chunkTarget)
	for _, file := range filesToWrite {
		if file.Flags&0x40 != 0 {
			continue // directory
		}
		path := filepath.Join(opts.DestDir, filepath.FromSlash(file.Filename))
		for _, chunk := range file.Chunks {
			cid := chunk.ChunkIDHex()
			chunkTargets[cid] = append(chunkTargets[cid], chunkTarget{
				path:   path,
				offset: chunk.Offset,
			})
		}
	}

	// File handle cache — avoid opening/closing the same file for every chunk.
	// Files are written by multiple goroutines so each write uses pwrite (WriteAt)
	// which is safe for concurrent access to different offsets.
	fileCache := &fileHandleCache{handles: make(map[string]*os.File)}
	defer fileCache.closeAll()

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
				// Write chunk data directly to all target files at the correct offsets
				cid := chunk.ChunkIDHex()
				targets, ok := chunkTargets[cid]
				if !ok {
					return nil // orphan chunk (shouldn't happen)
				}

				for _, target := range targets {
					f, err := fileCache.get(target.path)
					if err != nil {
						return fmt.Errorf("open %s: %w", target.path, err)
					}
					if _, err := f.WriteAt(data, int64(target.offset)); err != nil {
						return fmt.Errorf("write %s at %d: %w", target.path, target.offset, err)
					}
				}

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

	fileCache.closeAll()

	// Set file permissions — all files 0755 since Steam manifests don't carry permission info
	filesWritten := 0
	for _, file := range filesToWrite {
		if file.Flags&0x40 != 0 {
			continue
		}
		path := filepath.Join(opts.DestDir, filepath.FromSlash(file.Filename))
		os.Chmod(path, 0o755)
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

// fileHandleCache keeps files open during chunk download to avoid repeated open/close.
// WriteAt is safe for concurrent use on different offsets (pwrite syscall).
type fileHandleCache struct {
	mu      sync.Mutex
	handles map[string]*os.File
}

func (c *fileHandleCache) get(path string) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if f, ok := c.handles[path]; ok {
		return f, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		return nil, err
	}
	c.handles[path] = f
	return f, nil
}

func (c *fileHandleCache) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, f := range c.handles {
		f.Close()
	}
	c.handles = make(map[string]*os.File)
}
