package auth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyFile := filepath.Join(dir, "key")
	if e := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewSecretStore(keyFile, dir)
	if e != nil {
		t.Fatal(e)
	}
	secret, e := s.Generate()
	if e != nil {
		t.Fatal(e)
	}
	enc, e := s.Encrypt(secret)
	if e != nil {
		t.Fatal(e)
	}
	if string(enc) == secret {
		t.Fatal("secret not encrypted")
	}
	got, e := s.Decrypt(enc)
	if e != nil || got != secret {
		t.Fatalf("roundtrip: %q %v", got, e)
	}
	id := "123e4567-e89b-42d3-a456-426614174000"
	p, e := s.Materialize(id, secret)
	if e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0400 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if e = s.Remove("../outside"); e == nil {
		t.Fatal("unsafe deployment secret path accepted")
	}
}
