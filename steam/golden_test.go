package steam

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden file tests use real data captured from Steam's CDN.
// The test data in testdata/ was downloaded from Rust's dedicated server depot
// using capture_chunk.go and contains:
//   - chunk_raw.bin: raw encrypted+compressed chunk from the CDN
//   - chunk_meta.json: depot key, chunk ID, checksum, and expected sizes

type goldenChunkMeta struct {
	DepotID          uint32 `json:"depot_id"`
	DepotKeyHex      string `json:"depot_key_hex"`
	ChunkIDHex       string `json:"chunk_id_hex"`
	Checksum         uint32 `json:"checksum"`
	DecompressedSize uint32 `json:"decompressed_size"`
	CompressedSize   uint32 `json:"compressed_size"`
}

func loadGoldenChunk(t *testing.T) (raw []byte, meta goldenChunkMeta) {
	t.Helper()

	rawData, err := os.ReadFile("testdata/chunk_raw.bin")
	require.NoError(t, err, "golden test data missing — run capture_chunk.go to generate")

	metaData, err := os.ReadFile("testdata/chunk_meta.json")
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(metaData, &meta))
	return rawData, meta
}

func TestGolden_DecryptAndDecompress(t *testing.T) {
	raw, meta := loadGoldenChunk(t)

	depotKey, err := hex.DecodeString(meta.DepotKeyHex)
	require.NoError(t, err)

	// Full pipeline: decrypt → decompress
	data, err := DecryptDepotChunk(raw, depotKey)
	require.NoError(t, err, "decrypt+decompress should succeed on real chunk data")

	// Verify decompressed size matches manifest
	assert.Equal(t, meta.DecompressedSize, uint32(len(data)),
		"decompressed size should match manifest")

	// Verify checksum (Adler32 seed 0)
	assert.Equal(t, meta.Checksum, adler32Seed0(data),
		"checksum should match manifest (Adler32 seed 0)")
}

func TestGolden_DecryptOnly(t *testing.T) {
	raw, meta := loadGoldenChunk(t)

	depotKey, err := hex.DecodeString(meta.DepotKeyHex)
	require.NoError(t, err)

	// Decrypt without decompression
	decrypted, err := steamDecrypt(raw, depotKey)
	require.NoError(t, err)

	// Decrypted data should be larger than 0 and start with a compression magic
	require.Greater(t, len(decrypted), 0)

	// Should be one of: VZip (VZa), VZstd (VSZa), PKZip (PK\x03\x04), or zlib (0x78)
	magic := decrypted[0]
	validMagic := magic == 'V' || magic == 'P' || magic == 0x78
	assert.True(t, validMagic, "decrypted data should start with a known compression magic, got 0x%02x", magic)
}

func TestGolden_WrongKey(t *testing.T) {
	raw, _ := loadGoldenChunk(t)

	// Use a different key — should either error or produce wrong-sized output
	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = 0xFF
	}

	data, err := DecryptDepotChunk(raw, wrongKey)
	if err == nil {
		// Decryption might "succeed" but produce garbage
		// The decompressor should fail or produce wrong size
		t.Logf("decryption with wrong key produced %d bytes (may be garbage)", len(data))
	}
	// Either way, it shouldn't match the expected checksum
	if err == nil && len(data) > 0 {
		assert.NotEqual(t, uint32(391), uint32(len(data)),
			"wrong key should not produce correct-sized output")
	}
}
