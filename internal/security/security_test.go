package security

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected PHC string: %s", h)
	}
	if err := ValidateHash(h); err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("correct horse", h)
	if err != nil || !ok {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Fatal("salts must differ")
	}
}

func TestMalformedHashes(t *testing.T) {
	good, _ := HashPassword("x")
	bad := []string{
		"", "plaintext", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=999999999,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=0,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0",
		good + "$extra",
	}
	for _, b := range bad {
		if err := ValidateHash(b); err == nil {
			t.Errorf("accepted %q", b)
		}
		if _, err := VerifyPassword("x", b); err == nil {
			t.Errorf("verify accepted %q", b)
		}
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	s, _ := GenerateSecret()
	key, err := ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Encrypt(key, "1//refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "refresh") {
		t.Fatal("ciphertext leaks plaintext")
	}
	pt, err := Decrypt(key, ct)
	if err != nil || pt != "1//refresh-token" {
		t.Fatalf("%q %v", pt, err)
	}
	ct2, _ := Encrypt(key, "1//refresh-token")
	if ct == ct2 {
		t.Fatal("nonce reuse")
	}
	other, _ := ParseKey(mustSecret())
	if _, err := Decrypt(other, ct); err == nil {
		t.Fatal("wrong key accepted")
	}
	tampered := ct[:len(ct)-3] + "AAA"
	if _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := Decrypt(key, "garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func mustSecret() string { s, _ := GenerateSecret(); return s }

func TestParseKeyStrict(t *testing.T) {
	for _, bad := range []string{"", "short", "not base64!!", "QUJD"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
