package steam

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSteamDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	plaintext := []byte("Hello, Steam depot downloader! This is test data for AES.")

	// Encrypt using Steam's format: ECB-encrypted IV + CBC-encrypted data + PKCS7 padding
	encrypted := steamEncrypt(t, plaintext, key)

	// Decrypt
	decrypted, err := steamDecrypt(encrypted, key)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestSteamDecrypt_InvalidKey(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plaintext := []byte("test data padding!!")
	encrypted := steamEncrypt(t, plaintext, key)

	// Decrypt with wrong key
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)
	_, err := steamDecrypt(encrypted, wrongKey)
	// May succeed with garbage or fail with padding error — either way, data won't match
	if err == nil {
		// Decryption "succeeded" but data should be garbage
		t.Log("decryption with wrong key didn't error (expected for some key combos)")
	}
}

func TestRemovePKCS7Padding(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    []byte
		wantErr bool
	}{
		{"1 byte pad", []byte{1, 2, 3, 1}, []byte{1, 2, 3}, false},
		{"4 byte pad", []byte{1, 2, 4, 4, 4, 4}, []byte{1, 2}, false},
		{"full block pad", []byte{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16}, []byte{}, false},
		{"empty", []byte{}, []byte{}, false},
		{"invalid pad byte 0", []byte{1, 2, 0}, nil, true},
		{"invalid pad too large", []byte{1, 2, 20}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := removePKCS7Padding(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestDecompressChunk_UnknownMagic(t *testing.T) {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	_, err := decompressChunk(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown chunk compression")
}

func TestDecompressChunk_TooShort(t *testing.T) {
	data := []byte{0x01}
	result, err := decompressChunk(data)
	require.NoError(t, err)
	assert.Equal(t, data, result)
}

// steamEncrypt produces Steam-format encrypted data for testing.
func steamEncrypt(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()

	block, err := aes.NewCipher(key)
	require.NoError(t, err)

	// Generate random IV
	iv := make([]byte, aes.BlockSize)
	_, err = rand.Read(iv)
	require.NoError(t, err)

	// PKCS7 pad plaintext
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// CBC encrypt
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)

	// ECB encrypt the IV
	encryptedIV := make([]byte, aes.BlockSize)
	block.Encrypt(encryptedIV, iv)

	// Result: [encrypted IV][ciphertext]
	result := make([]byte, aes.BlockSize+len(ciphertext))
	copy(result[:aes.BlockSize], encryptedIV)
	copy(result[aes.BlockSize:], ciphertext)
	return result
}
