package steam

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz/lzma"
)

// DecryptDepotChunk decrypts and decompresses a raw chunk downloaded from the CDN.
// The depot key is 32 bytes (AES-256). Returns the decompressed chunk data.
func DecryptDepotChunk(data, depotKey []byte) ([]byte, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("chunk data too short: %d bytes", len(data))
	}

	decrypted, err := steamDecrypt(data, depotKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt chunk: %w", err)
	}

	decompressed, err := decompressChunk(decrypted)
	if err != nil {
		return nil, fmt.Errorf("decompress chunk: %w", err)
	}

	return decompressed, nil
}

// DecryptManifestFilenames decrypts encrypted filenames in a manifest using the depot key.
// Filenames are base64-encoded encrypted blobs in the manifest protobuf.
func DecryptManifestFilename(encryptedName []byte, depotKey []byte) (string, error) {
	decrypted, err := steamDecrypt(encryptedName, depotKey)
	if err != nil {
		return "", err
	}
	// Decrypted filename is null-terminated UTF-16LE or UTF-8
	decrypted = bytes.TrimRight(decrypted, "\x00")
	return string(decrypted), nil
}

// steamDecrypt performs Steam's AES decryption:
// first 16 bytes are ECB-encrypted IV, remainder is CBC-encrypted with PKCS7 padding.
func steamDecrypt(data, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("depot key must be 32 bytes, got %d", len(key))
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("data too short for decryption: %d bytes", len(data))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// Decrypt the IV (first 16 bytes) using ECB
	iv := make([]byte, aes.BlockSize)
	block.Decrypt(iv, data[:aes.BlockSize])

	// Decrypt the rest using CBC
	ciphertext := data[aes.BlockSize:]
	if len(ciphertext) == 0 {
		return []byte{}, nil
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not aligned to block size: %d bytes", len(ciphertext))
	}

	plaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove PKCS7 padding
	plaintext, err = removePKCS7Padding(plaintext)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

func removePKCS7Padding(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, fmt.Errorf("invalid PKCS7 padding: %d", padLen)
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid PKCS7 padding bytes")
		}
	}
	return data[:len(data)-padLen], nil
}

// decompressChunk detects the compression format and decompresses.
func decompressChunk(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return data, nil
	}

	// Check magic bytes for compression format
	// VZstd: "VSZa" = 56 53 5A 61
	// VZip:  "VZa"  = 56 5A 61
	if data[0] == 'V' {
		if len(data) > 3 && data[1] == 'S' && data[2] == 'Z' && data[3] == 'a' {
			return decompressZstd(data)
		}
		if len(data) > 2 && data[1] == 'Z' && data[2] == 'a' {
			return decompressVZip(data)
		}
	}

	// PKZip
	if data[0] == 'P' && data[1] == 'K' && len(data) > 3 && data[2] == 0x03 && data[3] == 0x04 {
		return data, nil
	}

	// Zlib (0x78 is the zlib header)
	if data[0] == 0x78 {
		return decompressZlib(data)
	}

	// No known compression header, data is uncompressed
	return data, nil
}

// decompressZlib handles standard zlib compressed data.
func decompressZlib(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("zlib reader: %w", err)
	}
	defer r.Close()
	return io.ReadAll(r)
}

// decompressVZip handles Steam's VZip (LZMA) format:
//
//	Header (7 bytes):  [2 "VZ"][1 version 'a'][4 CRC/timestamp]
//	LZMA props (5):    [1 prop byte][4 dictionary size LE]
//	Compressed data:   [variable]
//	Footer (10 bytes): [4 CRC32 of decompressed][4 decompressed size LE][2 magic 0x7A76]
func decompressVZip(data []byte) ([]byte, error) {
	// Header(7) + Props(5) + Footer(10) = 22 minimum
	if len(data) < 22 {
		return nil, fmt.Errorf("VZip data too short: %d bytes", len(data))
	}

	// Footer: last 10 bytes
	footer := data[len(data)-10:]
	decompressedSize := binary.LittleEndian.Uint32(footer[4:8])

	// LZMA properties start at offset 7 (after VZip header)
	// Compressed data starts at offset 12 (after 5 bytes of LZMA props)
	lzmaProps := data[7:12]       // 5 bytes: 1 prop + 4 dict size
	compressedData := data[12 : len(data)-10]

	// Build a standard LZMA stream: [5 props][8 uncompressed size LE][compressed]
	stream := make([]byte, 13+len(compressedData))
	copy(stream[:5], lzmaProps)
	binary.LittleEndian.PutUint64(stream[5:13], uint64(decompressedSize))
	copy(stream[13:], compressedData)

	reader, err := lzma.NewReader(bytes.NewReader(stream))
	if err != nil {
		return nil, fmt.Errorf("lzma reader: %w", err)
	}

	result, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("lzma decompress: %w", err)
	}

	return result, nil
}

// decompressZstd handles Steam's VZstd format:
//
//	Header (8 bytes):  [4 magic "VSZa"][4 CRC32]
//	Compressed data:   [variable, zstd frame]
//	Footer (15 bytes): [4 CRC32][4 decompressed size][4 reserved][3 magic "zsv"]
func decompressZstd(data []byte) ([]byte, error) {
	// Header(8) + Footer(15) = 23 minimum
	if len(data) < 23 {
		return nil, fmt.Errorf("VZstd data too short: %d bytes", len(data))
	}

	compressedData := data[8 : len(data)-15]

	decoder, err := zstd.NewReader(bytes.NewReader(compressedData))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer decoder.Close()

	result, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}

	return result, nil
}
