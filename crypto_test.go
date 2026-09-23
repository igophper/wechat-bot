package wechat

import (
	"bytes"
	"crypto/aes"
	"errors"
	"testing"
)

func TestUUIDAndUIN(t *testing.T) {
	uuid1 := GenerateUUID()
	uuid2 := GenerateUUID()
	if uuid1 == "" || uuid2 == "" || uuid1 == uuid2 {
		t.Fatalf("unexpected UUID generation: %q vs %q", uuid1, uuid2)
	}

	uin := GenerateUIN()
	if uin == "" {
		t.Fatal("empty UIN generated")
	}
}

func TestPKCS7Padding(t *testing.T) {
	blockSize := 16
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("hello")},
		{"exact_block", bytes.Repeat([]byte("a"), 16)},
		{"multiple_blocks", bytes.Repeat([]byte("b"), 32)},
		{"block_plus_one", bytes.Repeat([]byte("c"), 17)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			padded, err := PKCS7Pad(tt.data, blockSize)
			if err != nil {
				t.Fatal(err)
			}
			if len(padded)%blockSize != 0 {
				t.Fatalf("padded length %d is not multiple of block size %d", len(padded), blockSize)
			}
			unpadded, err := PKCS7Unpad(padded, blockSize)
			if err != nil {
				t.Fatalf("unpad failed: %v", err)
			}
			if !bytes.Equal(unpadded, tt.data) {
				t.Fatalf("unpadded data mismatch: got %v, want %v", unpadded, tt.data)
			}
		})
	}
}

func TestPKCS7InvalidBlockSizes(t *testing.T) {
	for _, size := range []int{-1, 0, 256} {
		if _, err := PKCS7Pad([]byte("data"), size); !errors.Is(err, ErrInvalidBlockSize) {
			t.Fatalf("Pad block size %d: %v", size, err)
		}
		if _, err := PKCS7Unpad([]byte("data"), size); !errors.Is(err, ErrInvalidBlockSize) {
			t.Fatalf("Unpad block size %d: %v", size, err)
		}
	}
}

func TestAESECBDecryptRejectsInvalidPadding(t *testing.T) {
	key := []byte("0123456789abcdef")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	// Encrypt one block without valid padding to deterministically simulate corruption.
	ciphertext := make([]byte, aes.BlockSize)
	block.Encrypt(ciphertext, make([]byte, aes.BlockSize))
	if got, err := AESECBDecrypt(ciphertext, key); !errors.Is(err, ErrInvalidPadding) || got != nil {
		t.Fatalf("invalid padding returned plaintext=%x err=%v", got, err)
	}
}

func TestGenerateRandomBytesRejectsNegativeSize(t *testing.T) {
	if _, err := GenerateRandomBytes(-1); err == nil {
		t.Fatal("expected an error for a negative size")
	}
}

func TestPKCS7UnpadErrors(t *testing.T) {
	blockSize := 16

	// Empty data
	if _, err := PKCS7Unpad([]byte{}, blockSize); err == nil {
		t.Fatal("expected error for empty data")
	}

	// Not aligned
	if _, err := PKCS7Unpad([]byte("not aligned"), blockSize); err == nil {
		t.Fatal("expected error for unaligned data")
	}

	// Invalid pad value (0)
	invalidPad := make([]byte, 16)
	if _, err := PKCS7Unpad(invalidPad, blockSize); err == nil {
		t.Fatal("expected error for 0 pad value")
	}

	// Inconsistent padding bytes
	inconsistent := bytes.Repeat([]byte{5}, 16)
	inconsistent[15] = 4
	if _, err := PKCS7Unpad(inconsistent, blockSize); err == nil {
		t.Fatal("expected error for inconsistent padding bytes")
	}
}

func TestAESECBEncryptDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes for AES-128
	payloads := [][]byte{
		[]byte("hello world"),
		[]byte(""),
		bytes.Repeat([]byte("large message payload with various chars 123!@#$%^&*()"), 50),
	}

	for _, p := range payloads {
		enc, err := AESECBEncrypt(p, key)
		if err != nil {
			t.Fatalf("encrypt failed: %v", err)
		}
		if len(enc)%16 != 0 {
			t.Fatalf("ciphertext not block aligned: %d", len(enc))
		}

		dec, err := AESECBDecrypt(enc, key)
		if err != nil {
			t.Fatalf("decrypt failed: %v", err)
		}
		if !bytes.Equal(dec, p) {
			t.Fatalf("decrypted mismatch: got %q, want %q", dec, p)
		}
	}
}
