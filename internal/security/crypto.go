package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const keyLen = 32

// ParseKey decodes ENCRYPTION_KEY: standard base64 of exactly 32 random bytes
// (generate with `mailroutine generate-secret` or `openssl rand -base64 32`).
func ParseKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, errors.New("must be standard base64 (generate with: openssl rand -base64 32)")
	}
	if len(b) != keyLen {
		return nil, fmt.Errorf("must decode to exactly %d bytes, got %d", keyLen, len(b))
	}
	return b, nil
}

// GenerateSecret returns 32 random bytes as standard base64.
func GenerateSecret() (string, error) {
	b := make([]byte, keyLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

const encPrefix = "v1:"

// associatedData binds ciphertexts to their purpose.
var associatedData = []byte("mailroutine/refresh-token/v1")

// Encrypt seals plaintext with AES-256-GCM. Output: "v1:" + base64(nonce||ciphertext).
func Encrypt(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), associatedData)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. It fails if the key is wrong or data was altered.
func Decrypt(key []byte, encoded string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(encoded, encPrefix) {
		return "", errors.New("unsupported ciphertext format")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encPrefix))
	if err != nil || len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("malformed ciphertext")
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], associatedData)
	if err != nil {
		return "", errors.New("decryption failed (wrong ENCRYPTION_KEY or corrupted data)")
	}
	return string(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// RandomToken returns n random bytes as unpadded URL-safe base64.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomID returns a 128-bit random hex identifier.
func RandomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// HMACHex returns hex(HMAC-SHA256(key, parts joined by NUL)).
func HMACHex(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	for i, p := range parts {
		if i > 0 {
			m.Write([]byte{0})
		}
		m.Write([]byte(p))
	}
	return hex.EncodeToString(m.Sum(nil))
}

// ConstantTimeEqual compares two strings without leaking their contents via timing.
func ConstantTimeEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}
