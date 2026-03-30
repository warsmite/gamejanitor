package steam

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/warsmite/gamejanitor/steam/proto"
	goproto "google.golang.org/protobuf/proto"
)

const manifestVersion = 5

// Magic values for manifest binary sections.
const (
	manifestPayloadMagic   uint32 = 0x71F617D0
	manifestMetadataMagic  uint32 = 0x1F4812BE
	manifestSignatureMagic uint32 = 0x1B81B817
	manifestEndMagic       uint32 = 0x32C415AB
)

// Manifest represents a parsed Steam depot manifest containing file listings and chunk info.
type Manifest struct {
	DepotID    uint32
	ManifestID uint64
	CreatedAt  uint32
	Files      []ManifestFile
}

// ManifestFile is a single file entry in a manifest.
type ManifestFile struct {
	Filename    string
	Size        uint64
	Flags       uint32
	SHAContent  []byte // SHA-1 of the full file content
	Chunks      []ManifestChunk
}

// ManifestChunk describes a chunk of a file.
type ManifestChunk struct {
	ChunkID          []byte // SHA-1, 20 bytes — used as the download identifier
	Checksum         uint32 // Adler32 of decompressed data
	Offset           uint64
	DecompressedSize uint32
	CompressedSize   uint32
}

// ChunkIDHex returns the hex-encoded chunk ID for CDN download URLs.
func (c ManifestChunk) ChunkIDHex() string {
	return hex.EncodeToString(c.ChunkID)
}

// GetManifestRequestCode requests a time-limited code needed to download a manifest from the CDN.
func (c *Client) GetManifestRequestCode(ctx context.Context, appID, depotID uint32, manifestID uint64, branch string) (uint64, error) {
	body := &proto.CContentServerDirectory_GetManifestRequestCode_Request{
		AppId:      &appID,
		DepotId:    &depotID,
		ManifestId: &manifestID,
	}
	if branch != "" && branch != "public" {
		body.AppBranch = &branch
	}

	resp, err := c.SendServiceMethod(ctx, "ContentServerDirectory.GetManifestRequestCode#1", body, true)
	if err != nil {
		return 0, fmt.Errorf("manifest request code: %w", err)
	}

	codeResp := &proto.CContentServerDirectory_GetManifestRequestCode_Response{}
	if err := goproto.Unmarshal(resp.Body, codeResp); err != nil {
		return 0, fmt.Errorf("unmarshal request code: %w", err)
	}

	return codeResp.GetManifestRequestCode(), nil
}

// GetCDNServers fetches a list of CDN content servers for downloading depot content.
func (c *Client) GetCDNServers(ctx context.Context, cellID uint32) ([]string, error) {
	body := &proto.CContentServerDirectory_GetServersForSteamPipe_Request{
		CellId: &cellID,
	}

	resp, err := c.SendServiceMethod(ctx, "ContentServerDirectory.GetServersForSteamPipe#1", body, true)
	if err != nil {
		return nil, fmt.Errorf("get CDN servers: %w", err)
	}

	serversResp := &proto.CContentServerDirectory_GetServersForSteamPipe_Response{}
	if err := goproto.Unmarshal(resp.Body, serversResp); err != nil {
		return nil, fmt.Errorf("unmarshal CDN servers: %w", err)
	}

	// Only use Valve's own CDN hosts with valid TLS certs.
	// Third-party edge servers (edgenext, alibaba, etc.) frequently have
	// certificate mismatches and TLS errors.
	var hosts []string
	for _, server := range serversResp.GetServers() {
		host := server.GetVhost()
		if host == "" {
			host = server.GetHost()
		}
		if host == "" {
			continue
		}
		// Only keep hosts under steamcontent.com that aren't known-bad third-party edges
		if strings.HasSuffix(host, ".steamcontent.com") &&
			!strings.Contains(host, "edgenext") &&
			!strings.Contains(host, "alibaba") {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no CDN servers returned")
	}

	return hosts, nil
}

// DownloadManifest fetches and parses a manifest from the CDN, trying multiple hosts.
func DownloadManifest(ctx context.Context, cdnHosts []string, depotID uint32, manifestID, requestCode uint64, depotKey []byte) (*Manifest, error) {
	var lastErr error
	for _, host := range cdnHosts {
		manifest, err := downloadManifestFromHost(ctx, host, depotID, manifestID, requestCode, depotKey)
		if err != nil {
			lastErr = err
			continue
		}
		return manifest, nil
	}
	return nil, fmt.Errorf("all CDN hosts failed for manifest %d: %w", manifestID, lastErr)
}

func downloadManifestFromHost(ctx context.Context, cdnHost string, depotID uint32, manifestID, requestCode uint64, depotKey []byte) (*Manifest, error) {
	url := fmt.Sprintf("https://%s/depot/%d/manifest/%d/%d/%d", cdnHost, depotID, manifestID, manifestVersion, requestCode)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest download returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manifest body: %w", err)
	}

	return ParseManifest(data, depotID, manifestID, depotKey)
}

// ParseManifest parses a manifest from its raw downloaded form (ZIP containing binary protobuf).
func ParseManifest(data []byte, depotID uint32, manifestID uint64, depotKey []byte) (*Manifest, error) {
	// The manifest is a ZIP file containing a single entry.
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open manifest zip: %w", err)
	}

	if len(zipReader.File) == 0 {
		return nil, fmt.Errorf("manifest zip is empty")
	}

	f, err := zipReader.File[0].Open()
	if err != nil {
		return nil, fmt.Errorf("open manifest entry: %w", err)
	}
	defer f.Close()

	manifestData, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read manifest entry: %w", err)
	}

	return parseManifestBinary(manifestData, depotID, manifestID, depotKey)
}

// parseManifestBinary parses the binary manifest format:
// repeated [4 bytes magic][4 bytes payload_len][payload_len bytes data]
func parseManifestBinary(data []byte, depotID uint32, manifestID uint64, depotKey []byte) (*Manifest, error) {
	manifest := &Manifest{
		DepotID:    depotID,
		ManifestID: manifestID,
	}

	// First pass: extract all sections
	var payloadProto *proto.ContentManifestPayload
	var filenamesEncrypted bool

	pos := 0
	for pos+8 <= len(data) {
		magic := binary.LittleEndian.Uint32(data[pos : pos+4])
		payloadLen := binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		pos += 8

		if magic == manifestEndMagic {
			break
		}

		if uint32(len(data)-pos) < payloadLen {
			return nil, fmt.Errorf("manifest section truncated at magic 0x%08X", magic)
		}

		payload := data[pos : pos+int(payloadLen)]
		pos += int(payloadLen)

		switch magic {
		case manifestPayloadMagic:
			payloadProto = &proto.ContentManifestPayload{}
			if err := goproto.Unmarshal(payload, payloadProto); err != nil {
				return nil, fmt.Errorf("unmarshal manifest payload: %w", err)
			}

		case manifestMetadataMagic:
			meta := &proto.ContentManifestMetadata{}
			if err := goproto.Unmarshal(payload, meta); err != nil {
				return nil, fmt.Errorf("unmarshal manifest metadata: %w", err)
			}
			manifest.CreatedAt = meta.GetCreationTime()
			if meta.GetDepotId() != 0 {
				manifest.DepotID = meta.GetDepotId()
			}
			filenamesEncrypted = meta.GetFilenamesEncrypted()

		case manifestSignatureMagic:
			continue
		}
	}

	if payloadProto == nil {
		return nil, fmt.Errorf("manifest has no payload section")
	}

	// Second pass: convert file mappings, decrypting filenames if needed
	for _, mapping := range payloadProto.GetMappings() {
		file, err := convertFileMapping(mapping, depotKey, filenamesEncrypted)
		if err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, *file)
	}

	return manifest, nil
}

func convertFileMapping(mapping *proto.ContentManifestPayload_FileMapping, depotKey []byte, filenamesEncrypted bool) (*ManifestFile, error) {
	filename := mapping.GetFilename()

	// Decrypt filenames if the manifest metadata says they're encrypted.
	// Encrypted filenames are base64-encoded, then AES-encrypted with the depot key.
	if filenamesEncrypted && depotKey != nil {
		encBytes, err := base64.StdEncoding.DecodeString(filename)
		if err != nil {
			return nil, fmt.Errorf("base64 decode filename: %w", err)
		}
		decrypted, err := DecryptManifestFilename(encBytes, depotKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt filename: %w", err)
		}
		// Steam stores paths with Windows separators — normalize to forward slashes
		filename = strings.ReplaceAll(decrypted, "\\", "/")
	}

	file := &ManifestFile{
		Filename:   filename,
		Size:       mapping.GetSize(),
		Flags:      mapping.GetFlags(),
		SHAContent: mapping.GetShaContent(),
	}

	for _, chunk := range mapping.GetChunks() {
		file.Chunks = append(file.Chunks, ManifestChunk{
			ChunkID:          chunk.GetSha(),
			Checksum:         chunk.GetCrc(),
			Offset:           chunk.GetOffset(),
			DecompressedSize: chunk.GetCbOriginal(),
			CompressedSize:   chunk.GetCbCompressed(),
		})
	}

	return file, nil
}

// DiffManifests compares an old manifest against a new one and returns
// which chunks need to be downloaded (new or changed) and which files were removed.
type ManifestDiff struct {
	// ChunksToDownload is the set of chunk IDs (hex) that exist in the new manifest
	// but not in the old one. Maps chunk ID hex → the chunk info.
	ChunksToDownload map[string]ManifestChunk
	// FilesRemoved lists filenames that were in the old manifest but not the new one.
	FilesRemoved []string
	// FilesChanged lists filenames that have different content between manifests.
	FilesChanged []string
	// FilesAdded lists filenames that are new in the new manifest.
	FilesAdded []string
	// Unchanged is the count of files that didn't change.
	Unchanged int
}

// DiffManifests computes the delta between two manifests for efficient updates.
func DiffManifests(oldManifest, newManifest *Manifest) *ManifestDiff {
	diff := &ManifestDiff{
		ChunksToDownload: make(map[string]ManifestChunk),
	}

	// Build lookup of old files and their chunks
	oldFiles := make(map[string]*ManifestFile, len(oldManifest.Files))
	oldChunks := make(map[string]struct{})
	for i := range oldManifest.Files {
		f := &oldManifest.Files[i]
		oldFiles[f.Filename] = f
		for _, c := range f.Chunks {
			oldChunks[c.ChunkIDHex()] = struct{}{}
		}
	}

	// Compare new manifest against old
	newFiles := make(map[string]struct{}, len(newManifest.Files))
	for _, f := range newManifest.Files {
		newFiles[f.Filename] = struct{}{}

		oldFile, existed := oldFiles[f.Filename]
		if !existed {
			diff.FilesAdded = append(diff.FilesAdded, f.Filename)
		} else if !bytes.Equal(f.SHAContent, oldFile.SHAContent) {
			diff.FilesChanged = append(diff.FilesChanged, f.Filename)
		} else {
			diff.Unchanged++
			continue
		}

		// Collect chunks that don't exist in the old manifest
		for _, c := range f.Chunks {
			cid := c.ChunkIDHex()
			if _, have := oldChunks[cid]; !have {
				diff.ChunksToDownload[cid] = c
			}
		}
	}

	// Find removed files
	for name := range oldFiles {
		if _, exists := newFiles[name]; !exists {
			diff.FilesRemoved = append(diff.FilesRemoved, name)
		}
	}

	return diff
}
