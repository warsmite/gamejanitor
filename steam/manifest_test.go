package steam

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiffManifests_FullDownload(t *testing.T) {
	old := &Manifest{}
	new := &Manifest{
		Files: []ManifestFile{
			{Filename: "game.exe", SHAContent: []byte{1}, Chunks: []ManifestChunk{{ChunkID: []byte{0xAA}}}},
			{Filename: "data.pak", SHAContent: []byte{2}, Chunks: []ManifestChunk{{ChunkID: []byte{0xBB}}}},
		},
	}

	diff := DiffManifests(old, new)
	assert.Len(t, diff.FilesAdded, 2)
	assert.Empty(t, diff.FilesRemoved)
	assert.Empty(t, diff.FilesChanged)
	assert.Equal(t, 0, diff.Unchanged)
	assert.Len(t, diff.ChunksToDownload, 2)
}

func TestDiffManifests_NoChanges(t *testing.T) {
	sha := []byte{1, 2, 3}
	manifest := &Manifest{
		Files: []ManifestFile{
			{Filename: "game.exe", SHAContent: sha, Chunks: []ManifestChunk{{ChunkID: []byte{0xAA}}}},
		},
	}

	diff := DiffManifests(manifest, manifest)
	assert.Empty(t, diff.FilesAdded)
	assert.Empty(t, diff.FilesRemoved)
	assert.Empty(t, diff.FilesChanged)
	assert.Equal(t, 1, diff.Unchanged)
	assert.Empty(t, diff.ChunksToDownload)
}

func TestDiffManifests_FileChanged(t *testing.T) {
	old := &Manifest{
		Files: []ManifestFile{
			{Filename: "game.exe", SHAContent: []byte{1}, Chunks: []ManifestChunk{{ChunkID: []byte{0xAA}}}},
		},
	}
	new := &Manifest{
		Files: []ManifestFile{
			{Filename: "game.exe", SHAContent: []byte{2}, Chunks: []ManifestChunk{{ChunkID: []byte{0xBB}}}},
		},
	}

	diff := DiffManifests(old, new)
	assert.Empty(t, diff.FilesAdded)
	assert.Empty(t, diff.FilesRemoved)
	assert.Len(t, diff.FilesChanged, 1)
	assert.Equal(t, "game.exe", diff.FilesChanged[0])
	assert.Len(t, diff.ChunksToDownload, 1)
}

func TestDiffManifests_FileRemoved(t *testing.T) {
	old := &Manifest{
		Files: []ManifestFile{
			{Filename: "old.txt", SHAContent: []byte{1}},
			{Filename: "keep.txt", SHAContent: []byte{2}},
		},
	}
	new := &Manifest{
		Files: []ManifestFile{
			{Filename: "keep.txt", SHAContent: []byte{2}},
		},
	}

	diff := DiffManifests(old, new)
	assert.Empty(t, diff.FilesAdded)
	assert.Len(t, diff.FilesRemoved, 1)
	assert.Equal(t, "old.txt", diff.FilesRemoved[0])
	assert.Equal(t, 1, diff.Unchanged)
}

func TestDiffManifests_SharedChunks(t *testing.T) {
	sharedChunk := ManifestChunk{ChunkID: []byte{0xCC}}
	old := &Manifest{
		Files: []ManifestFile{
			{Filename: "a.txt", SHAContent: []byte{1}, Chunks: []ManifestChunk{sharedChunk}},
		},
	}
	new := &Manifest{
		Files: []ManifestFile{
			{Filename: "a.txt", SHAContent: []byte{2}, Chunks: []ManifestChunk{sharedChunk, {ChunkID: []byte{0xDD}}}},
		},
	}

	diff := DiffManifests(old, new)
	// Shared chunk already exists in old manifest — only the new chunk should be downloaded
	assert.Len(t, diff.ChunksToDownload, 1)
	assert.Equal(t, "dd", diff.ChunksToDownload["dd"].ChunkIDHex())
}
