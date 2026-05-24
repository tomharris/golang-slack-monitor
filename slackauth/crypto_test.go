package slackauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

// RFC 6070 PBKDF2-HMAC-SHA1 known-answer vectors.
func TestPBKDF2SHA1_RFC6070(t *testing.T) {
	tests := []struct {
		password, salt string
		iter, keyLen   int
		wantHex        string
	}{
		{"password", "salt", 1, 20, "0c60c80f961f0e71f3a9b524af6012062fe037a6"},
		{"password", "salt", 2, 20, "ea6c014dc72d6f8ccd1ed92ace1d41f0d8de8957"},
	}
	for _, tt := range tests {
		got := pbkdf2SHA1([]byte(tt.password), []byte(tt.salt), tt.iter, tt.keyLen)
		if hex.EncodeToString(got) != tt.wantHex {
			t.Errorf("pbkdf2SHA1(%q,%q,%d,%d) = %x, want %s", tt.password, tt.salt, tt.iter, tt.keyLen, got, tt.wantHex)
		}
	}
}

func TestDecryptCookie_RoundTrip(t *testing.T) {
	// Derive the same key decryptCookie will derive for password "peanuts".
	key := pbkdf2SHA1([]byte("peanuts"), []byte("saltysalt"), 1003, 16)
	plain := "xoxd-abc%2Fdef" // url-encoded value as stored

	block, _ := aes.NewCipher(key)
	iv := bytes.Repeat([]byte{' '}, 16)
	// PKCS7 pad
	padded := pkcs7Pad([]byte(plain), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	encrypted := append([]byte("v10"), ct...)

	got, err := decryptCookie(encrypted, "peanuts")
	if err != nil {
		t.Fatalf("decryptCookie: %v", err)
	}
	if got != "xoxd-abc/def" { // url-decoded
		t.Errorf("decryptCookie = %q, want %q", got, "xoxd-abc/def")
	}
}

func TestDecryptCookie_StripsDomainHashPrefix(t *testing.T) {
	key := pbkdf2SHA1([]byte("peanuts"), []byte("saltysalt"), 1003, 16)
	// 32-byte domain hash prefix followed by the real value.
	plain := append(bytes.Repeat([]byte{0xAB}, 32), []byte("xoxd-zzz")...)

	block, _ := aes.NewCipher(key)
	iv := bytes.Repeat([]byte{' '}, 16)
	padded := pkcs7Pad(plain, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	encrypted := append([]byte("v10"), ct...)

	got, err := decryptCookie(encrypted, "peanuts")
	if err != nil {
		t.Fatalf("decryptCookie: %v", err)
	}
	if got != "xoxd-zzz" {
		t.Errorf("decryptCookie = %q, want %q", got, "xoxd-zzz")
	}
}

// helper mirrored from implementation for test setup
func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func makeEncryptedCookie(t *testing.T, password, urlEncodedValue string) []byte {
	t.Helper()
	key := pbkdf2SHA1([]byte(password), []byte("saltysalt"), 1003, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{' '}, 16)
	padded := pkcs7Pad([]byte(urlEncodedValue), aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return append([]byte("v10"), ct...)
}
