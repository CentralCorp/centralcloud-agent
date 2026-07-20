package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
)

type SecretStore struct {
	key        []byte
	runtimeDir string
}

func NewSecretStore(keyFile, runtimeDir string) (*SecretStore, error) {
	b, err := os.ReadFile(keyFile) // #nosec G304 -- keyFile is an administrator-supplied configuration path.
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	b = []byte(strings.TrimSpace(string(b)))
	if decoded, e := base64.StdEncoding.DecodeString(string(b)); e == nil && len(decoded) == 32 {
		b = decoded
	}
	if len(b) != 32 {
		return nil, errors.New("master key must contain exactly 32 raw bytes or base64 bytes")
	}
	return &SecretStore{key: append([]byte(nil), b...), runtimeDir: runtimeDir}, nil
}
func (s *SecretStore) Generate() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *SecretStore) Encrypt(value string) ([]byte, error) {
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	return g.Seal(n, n, []byte(value), nil), nil
}
func (s *SecretStore) Decrypt(value []byte) (string, error) {
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(value) < g.NonceSize() {
		return "", errors.New("invalid encrypted secret")
	}
	p, e := g.Open(nil, value[:g.NonceSize()], value[g.NonceSize():], nil)
	return string(p), e
}
func (s *SecretStore) Materialize(id, value string) (string, error) {
	return s.MaterializeNamed(id, "postgres_password", value)
}
func (s *SecretStore) MaterializeNamed(id, name, value string) (string, error) {
	dir, e := s.deploymentDirectory(id)
	if e != nil {
		return "", e
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	if filepath.Base(name) != name || name == "." || name == "" {
		return "", errors.New("invalid secret file name")
	}
	p := filepath.Join(dir, name)
	if e := os.WriteFile(p, []byte(value), 0400); e != nil {
		return "", e
	}
	return p, nil
}
func (s *SecretStore) RemoveNamed(id, name string) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return errors.New("invalid secret file name")
	}
	dir, e := s.deploymentDirectory(id)
	if e != nil {
		return e
	}
	err := os.Remove(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *SecretStore) Remove(id string) error {
	dir, e := s.deploymentDirectory(id)
	if e != nil {
		return e
	}
	return os.RemoveAll(dir)
}

func (s *SecretStore) deploymentDirectory(id string) (string, error) {
	id = strings.ToLower(id)
	if e := domain.ValidateDeploymentID(id); e != nil {
		return "", e
	}
	root := filepath.Clean(filepath.Join(s.runtimeDir, "deployments"))
	dir := filepath.Join(root, id)
	rel, e := filepath.Rel(root, dir)
	if e != nil || rel != id {
		return "", errors.New("deployment secret path escapes runtime directory")
	}
	return dir, nil
}
