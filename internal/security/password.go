// Package security contains password hashing, token encryption and random helpers.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Default Argon2id parameters (OWASP-recommended class: 64 MiB, 3 passes).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32

	// Upper bounds protect against hostile or corrupted hashes exhausting memory.
	maxArgonMemoryKiB = 1024 * 1024
	maxArgonTime      = 20
	maxArgonThreads   = 64
)

// ErrInvalidHash is returned for malformed or unsupported password hashes.
var ErrInvalidHash = errors.New("invalid argon2id PHC hash")

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

// HashPassword returns a PHC-formatted Argon2id hash with a random salt.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func parseHash(phc string) (*argonParams, error) {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, ErrInvalidHash
	}
	var p argonParams
	var threads uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &threads); err != nil || n != 3 {
		return nil, ErrInvalidHash
	}
	if p.memory < 8 || p.memory > maxArgonMemoryKiB || p.time < 1 || p.time > maxArgonTime || threads < 1 || threads > maxArgonThreads {
		return nil, ErrInvalidHash
	}
	p.threads = uint8(threads)
	var err error
	if p.salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[4]); err != nil || len(p.salt) < 8 {
		return nil, ErrInvalidHash
	}
	if p.key, err = base64.RawStdEncoding.Strict().DecodeString(parts[5]); err != nil || len(p.key) < 16 {
		return nil, ErrInvalidHash
	}
	return &p, nil
}

// ValidateHash reports whether phc is a well-formed Argon2id hash.
func ValidateHash(phc string) error {
	_, err := parseHash(phc)
	return err
}

// VerifyPassword checks password against an Argon2id PHC hash in constant time.
func VerifyPassword(password, phc string) (bool, error) {
	p, err := parseHash(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(got, p.key) == 1, nil
}
