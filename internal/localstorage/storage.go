package localstorage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
)

const ownerDirectory = ".centralcloud-owners"

type Manager struct {
	panelRoot  string
	backupRoot string
}

func New(panelRoot, backupRoot string) (*Manager, error) {
	panel, err := canonicalRoot(panelRoot)
	if err != nil {
		return nil, fmt.Errorf("panel storage root: %w", err)
	}
	backup, err := canonicalRoot(backupRoot)
	if err != nil {
		return nil, fmt.Errorf("backup storage root: %w", err)
	}
	if panel == backup || isStrictChild(panel, backup) || isStrictChild(backup, panel) {
		return nil, errors.New("panel and backup storage roots must be distinct and non-nested")
	}
	return &Manager{panelRoot: panel, backupRoot: backup}, nil
}

func (m *Manager) EnsurePanel(id string) (string, error) {
	return ensureOwnedDirectory(m.panelRoot, id)
}

func (m *Manager) EnsureBackup(id string) (string, error) {
	return ensureOwnedDirectory(m.backupRoot, id)
}

func (m *Manager) PurgePanel(id string) error {
	return purgeOwnedDirectory(m.panelRoot, id)
}

func (m *Manager) PurgeBackups(id string) error {
	return purgeOwnedDirectory(m.backupRoot, id)
}

func canonicalRoot(raw string) (string, error) {
	if raw == "" || !filepath.IsAbs(raw) {
		return "", errors.New("path must be absolute")
	}
	clean := filepath.Clean(raw)
	if clean == string(filepath.Separator) {
		return "", errors.New("filesystem root is not allowed")
	}
	if err := os.MkdirAll(clean, 0700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if resolved == string(filepath.Separator) {
		return "", errors.New("filesystem root is not allowed")
	}
	return filepath.Clean(resolved), nil
}

func ensureOwnedDirectory(root, id string) (string, error) {
	path, err := deploymentPath(root, id)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	case err != nil:
		return "", err
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return "", errors.New("deployment storage path is not a real directory")
	}
	info, err = os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("deployment storage path changed during validation")
	}
	if err = os.Chmod(path, 0700); err != nil {
		return "", err
	}
	if err = ensureMarker(root, id); err != nil {
		return "", err
	}
	return path, nil
}

func purgeOwnedDirectory(root, id string) error {
	path, err := deploymentPath(root, id)
	if err != nil {
		return err
	}
	quarantine := filepath.Join(root, ".purging-"+strings.ToLower(id))
	if !isStrictChild(root, quarantine) {
		return errors.New("invalid quarantine path")
	}
	pathExists, err := ownedDirectoryExists(root, path, id)
	if err != nil {
		return err
	}
	quarantineExists, err := ownedDirectoryExists(root, quarantine, id)
	if err != nil {
		return err
	}
	if pathExists && quarantineExists {
		return errors.New("both active and quarantined deployment storage exist")
	}
	if pathExists {
		if err = os.Rename(path, quarantine); err != nil {
			return err
		}
		quarantineExists = true
	}
	if !quarantineExists {
		if err = verifyMarker(root, id); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return os.Remove(markerPath(root, id))
	}
	if err = verifyMarker(root, id); err != nil {
		return err
	}
	if err = os.RemoveAll(quarantine); err != nil {
		return err
	}
	err = os.Remove(markerPath(root, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func deploymentPath(root, id string) (string, error) {
	id = strings.ToLower(id)
	if err := domain.ValidateDeploymentID(id); err != nil {
		return "", err
	}
	path := filepath.Join(root, id)
	if !isStrictChild(root, path) || filepath.Base(path) != id {
		return "", errors.New("deployment storage path escapes configured root")
	}
	return path, nil
}

func ownedDirectoryExists(root, path, id string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("owned path is not a real directory")
	}
	if err = verifyMarker(root, id); err != nil {
		return false, err
	}
	return true, nil
}

func ensureMarker(root, id string) error {
	owners := filepath.Join(root, ownerDirectory)
	if !isStrictChild(root, owners) {
		return errors.New("ownership directory escapes configured root")
	}
	if err := os.MkdirAll(owners, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(owners)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("ownership path is not a real directory")
	}
	marker := markerPath(root, id)
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- marker is below a validated deployment directory.
	if err == nil {
		if _, writeErr := file.WriteString(strings.ToLower(id) + "\n"); writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		return file.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	return verifyMarker(root, id)
}

func verifyMarker(root, id string) error {
	marker := markerPath(root, id)
	info, err := os.Lstat(marker)
	if err != nil {
		return fmt.Errorf("deployment ownership marker: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("deployment ownership marker is not a regular file")
	}
	value, err := os.ReadFile(marker) // #nosec G304 -- marker is below a validated deployment directory.
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(value)) != strings.ToLower(id) {
		return errors.New("deployment ownership marker does not match deployment_id")
	}
	return nil
}

func markerPath(root, id string) string {
	return filepath.Join(root, ownerDirectory, strings.ToLower(id))
}

func isStrictChild(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
