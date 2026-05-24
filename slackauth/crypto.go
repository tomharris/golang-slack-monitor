package slackauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
)

// pbkdf2SHA1 derives a key of keyLen bytes using PBKDF2-HMAC-SHA1.
// (crypto/pbkdf2 only exists in stdlib from Go 1.24+; this is kept explicit and
// dependency-free.)
func pbkdf2SHA1(password, salt []byte, iter, keyLen int) []byte {
	const hLen = sha1.Size // 20
	numBlocks := (keyLen + hLen - 1) / hLen
	var dk []byte
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha1.New, password)
		mac.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// decryptCookie decrypts a Chromium-style encrypted cookie value (macOS) using
// the "Slack Safe Storage" Keychain password, returning the url-decoded value.
func decryptCookie(encrypted []byte, password string) (string, error) {
	if !bytes.HasPrefix(encrypted, []byte("v10")) {
		return "", fmt.Errorf("unexpected cookie encryption version (want v10 prefix)")
	}
	ct := encrypted[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length %d", len(ct))
	}

	key := pbkdf2SHA1([]byte(password), []byte("saltysalt"), 1003, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)

	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}

	value := string(plain)
	// Newer Chromium prepends a 32-byte SHA256 domain hash to the plaintext.
	if !strings.HasPrefix(value, "xoxd-") && len(plain) > 32 {
		value = string(plain[32:])
	}

	unescaped, err := url.QueryUnescape(value)
	if err != nil {
		return "", fmt.Errorf("url-unescape cookie: %w", err)
	}
	if !strings.HasPrefix(unescaped, "xoxd-") {
		return "", fmt.Errorf("decrypted cookie does not look like an xoxd token")
	}
	return unescaped, nil
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded length %d", len(b))
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	return b[:len(b)-n], nil
}
