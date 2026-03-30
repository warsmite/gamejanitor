package steam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// DownloadUGCItem downloads a Steam Workshop UGC item to destDir.
// workshopDepotID is the depot that holds UGC content (from AppInfo.WorkshopDepotID or app ID as fallback).
// hcontentFile is the manifest ID for the Workshop item (from GetPublishedFileDetails API).
func (d *DepotDownloader) DownloadUGCItem(ctx context.Context, appID, workshopDepotID uint32, hcontentFile uint64, destDir string) error {
	depotKey, err := d.client.GetDepotDecryptionKey(ctx, workshopDepotID, appID)
	if err != nil {
		return fmt.Errorf("get workshop depot key: %w", err)
	}

	requestCode, err := d.client.GetManifestRequestCode(ctx, appID, workshopDepotID, hcontentFile, "public")
	if err != nil {
		return fmt.Errorf("get manifest request code for UGC %d: %w", hcontentFile, err)
	}

	if d.cdn == nil {
		hosts, err := d.client.GetCDNServers(ctx, 0)
		if err != nil {
			return fmt.Errorf("get CDN servers: %w", err)
		}
		d.cdn = NewCDNClient(d.log, hosts)
	}

	manifest, err := DownloadManifest(ctx, d.cdn.hosts, workshopDepotID, hcontentFile, requestCode, depotKey)
	if err != nil {
		return fmt.Errorf("download UGC manifest: %w", err)
	}

	d.log.Info("UGC manifest parsed",
		"hcontent_file", hcontentFile,
		"files", len(manifest.Files),
	)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	// Create directories and build chunk index
	chunkTargets := make(map[string][]chunkTarget)
	var chunksToDownload []ManifestChunk
	seen := make(map[string]struct{})

	for _, file := range manifest.Files {
		path := filepath.Join(destDir, filepath.FromSlash(file.Filename))

		if file.Flags&0x40 != 0 {
			os.MkdirAll(path, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(path), 0o755)

		for _, chunk := range file.Chunks {
			cid := chunk.ChunkIDHex()
			chunkTargets[cid] = append(chunkTargets[cid], chunkTarget{
				path:   path,
				offset: chunk.Offset,
			})
			if _, dup := seen[cid]; !dup {
				seen[cid] = struct{}{}
				chunksToDownload = append(chunksToDownload, chunk)
			}
		}
	}

	if len(chunksToDownload) == 0 {
		return nil
	}

	fileCache := &fileHandleCache{handles: make(map[string]*os.File)}
	defer fileCache.closeAll()

	err = d.cdn.DownloadChunksParallel(ctx, workshopDepotID, depotKey, chunksToDownload, d.Workers,
		func(chunk ManifestChunk, data []byte) error {
			for _, target := range chunkTargets[chunk.ChunkIDHex()] {
				f, err := fileCache.get(target.path)
				if err != nil {
					return fmt.Errorf("open %s: %w", target.path, err)
				}
				if _, err := f.WriteAt(data, int64(target.offset)); err != nil {
					return fmt.Errorf("write %s: %w", target.path, err)
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("download UGC chunks: %w", err)
	}

	fileCache.closeAll()

	for _, file := range manifest.Files {
		if file.Flags&0x40 != 0 {
			continue
		}
		os.Chmod(filepath.Join(destDir, filepath.FromSlash(file.Filename)), 0o755)
	}

	d.log.Info("UGC item downloaded",
		"hcontent_file", hcontentFile,
		"files", len(manifest.Files),
		"chunks", len(chunksToDownload),
	)

	return nil
}
