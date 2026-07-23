package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndSignManifest(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.json")
	signaturePath := manifestPath + ".sig"
	keyPath := filepath.Join(directory, "release.key")

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(privateKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := generate([]string{
		"-version", "1.3.0",
		"-base-url", "https://example.invalid/releases/1.3.0",
		"-amd64-sha256", sum,
		"-arm64-sha256", sum,
		"-published-at", "2026-07-23T00:00:00Z",
		"-output", manifestPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sign([]string{"-input", manifestPath, "-output", signaturePath, "-key-file", keyPath}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(manifestPath) // #nosec G304 -- isolated test path.
	if err != nil {
		t.Fatal(err)
	}
	var decoded manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != "1.3.0" || decoded.Assets["linux-arm64"].SHA256 != sum {
		t.Fatalf("unexpected manifest: %#v", decoded)
	}
	encodedSignature, err := os.ReadFile(signaturePath) // #nosec G304 -- isolated test path.
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(encodedSignature)))
	if err != nil || !ed25519.Verify(publicKey, data, signature) {
		t.Fatal("manifest signature is invalid")
	}
	for _, path := range []string{manifestPath, signaturePath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions are %o", path, info.Mode().Perm())
		}
	}
}
