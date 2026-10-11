package security

import (
	"strings"
	"testing"
)

func TestRandomHelpers(t *testing.T) {
	a, err := RandomToken(32)
	b, _ := RandomToken(32)
	if err != nil || a == b || len(a) != 43 {
		t.Fatalf("%q %q %v", a, b, err)
	}
	if id := RandomID(); len(id) != 32 || id == RandomID() {
		t.Fatal("RandomID must be 128 random bits as hex")
	}
}

func TestHMACAndCompare(t *testing.T) {
	k := []byte("key")
	if HMACHex(k, "a", "b") != HMACHex(k, "a", "b") {
		t.Fatal("not deterministic")
	}
	// Parts are NUL-separated so ("ab","c") != ("a","bc").
	if HMACHex(k, "ab", "c") == HMACHex(k, "a", "bc") {
		t.Fatal("ambiguous encoding")
	}
	if HMACHex([]byte("other"), "a") == HMACHex(k, "a") {
		t.Fatal("key ignored")
	}
	if !ConstantTimeEqual("x", "x") || ConstantTimeEqual("x", "y") || ConstantTimeEqual("x", "xx") {
		t.Fatal("ConstantTimeEqual")
	}
}

func TestEncryptRejectsBadKeyAndShortData(t *testing.T) {
	if _, err := Encrypt([]byte("short"), "x"); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := Decrypt([]byte("short"), "v1:AAAA"); err == nil {
		t.Fatal("short key accepted")
	}
	key := make([]byte, 32)
	for _, bad := range []string{"v1:", "v1:!!!", "v1:AAAA", "v2:AAAA"} {
		if _, err := Decrypt(key, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseKey(strings.Repeat("A", 44)); err == nil {
		t.Fatal("44 chars of A decodes to 33 bytes? must be rejected")
	}
}

func TestVerifyWithGarbageHash(t *testing.T) {
	if ok, err := VerifyPassword("x", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$bad!"); ok || err == nil {
		t.Fatal("accepted bad key encoding")
	}
	if _, err := VerifyPassword("x", "$argon2id$v=19$m=x,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"); err == nil {
		t.Fatal("accepted bad params")
	}
}
