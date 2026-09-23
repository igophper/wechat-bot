package wechat

import (
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrInvalidPadding is returned when PKCS#7 padding is invalid.
	ErrInvalidPadding = errors.New("wechat: invalid pkcs7 padding")
)

// GenerateRandomBytes generates n cryptographically secure random bytes.
func GenerateRandomBytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("random byte count must not be negative")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}
	return b, nil
}

// GenerateUUID generates a random UUID v4 string without external dependencies.
func GenerateUUID() string {
	var uuid [16]byte
	_, _ = io.ReadFull(rand.Reader, uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// GenerateUIN produces a randomized base64 string for the X-WECHAT-UIN header.
func GenerateUIN() string {
	var n uint32
	_ = binary.Read(rand.Reader, binary.LittleEndian, &n)
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", n)))
}

// PKCS7Pad adds PKCS#7 padding to data. The block size must be between 1 and 255.
func PKCS7Pad(data []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		return nil, ErrInvalidBlockSize
	}
	padLen := blockSize - (len(data) % blockSize)
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	return padded, nil
}

// PKCS7Unpad removes PKCS#7 padding from data.
func PKCS7Unpad(data []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		return nil, ErrInvalidBlockSize
	}
	if len(data) == 0 {
		return nil, ErrInvalidPadding
	}
	if len(data)%blockSize != 0 {
		return nil, ErrInvalidBlockSize
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, ErrInvalidPadding
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, ErrInvalidPadding
		}
	}
	return data[:len(data)-padLen], nil
}

// AESECBPaddedSize calculates the ciphertext size after PKCS#7 padding.
func AESECBPaddedSize(plaintextSize int) int {
	return (plaintextSize/aes.BlockSize + 1) * aes.BlockSize
}

// AESECBEncrypt encrypts plaintext with key using AES-ECB and PKCS#7 padding.
func AESECBEncrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded, err := PKCS7Pad(plaintext, aes.BlockSize)
	if err != nil {
		return nil, err
	}
	encrypted := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(encrypted[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return encrypted, nil
}

// AESECBDecrypt decrypts ciphertext with key using AES-ECB and removes PKCS#7 padding.
func AESECBDecrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrInvalidBlockSize
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return PKCS7Unpad(plaintext, aes.BlockSize)
}
