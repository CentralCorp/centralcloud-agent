package localstorage

import (
	"os"
	"path/filepath"
	"testing"
)

const testDeploymentID = "123e4567-e89b-42d3-a456-426614174000"

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	m, err := New(filepath.Join(root, "panels"), filepath.Join(root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsurePanelPreservesExistingDataAndCreatesMarker(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(m.panelRoot, testDeploymentID)
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(path, "customer-data")
	if err := os.WriteFile(data, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := m.EnsurePanel(testDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path=%q want=%q", got, path)
	}
	if content, err := os.ReadFile(data); err != nil || string(content) != "keep" {
		t.Fatalf("existing data changed: %q %v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	if err = verifyMarker(m.panelRoot, testDeploymentID); err != nil {
		t.Fatal(err)
	}
}

func TestPurgePanelIsIdempotentAndResumesQuarantine(t *testing.T) {
	m := testManager(t)
	path, err := m.EnsurePanel(testDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(m.panelRoot, ".purging-"+testDeploymentID)
	if err = os.Rename(path, quarantine); err != nil {
		t.Fatal(err)
	}
	if err = m.PurgePanel(testDeploymentID); err != nil {
		t.Fatal(err)
	}
	if err = m.PurgePanel(testDeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("quarantine remains: %v", err)
	}
}

func TestPurgeRejectsTraversalOutsideRootAndForeignMarker(t *testing.T) {
	m := testManager(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(m.panelRoot, testDeploymentID)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := m.PurgePanel(testDeploymentID); err == nil {
		t.Fatal("symlinked deployment directory was accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory was touched: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	_, err := m.EnsurePanel(testDeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(markerPath(m.panelRoot, testDeploymentID), []byte("123e4567-e89b-42d3-a456-426614174099\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.PurgePanel(testDeploymentID); err == nil {
		t.Fatal("foreign marker was accepted")
	}
	for _, id := range []string{"../outside", testDeploymentID + "/../../outside", "/"} {
		if _, err = m.EnsurePanel(id); err == nil {
			t.Fatalf("unsafe deployment id %q accepted", id)
		}
	}
}

func TestNewRejectsRootAndNestedRoots(t *testing.T) {
	if _, err := New("/", filepath.Join(t.TempDir(), "backups")); err == nil {
		t.Fatal("filesystem root accepted")
	}
	root := t.TempDir()
	if _, err := New(root, filepath.Join(root, "backups")); err == nil {
		t.Fatal("nested roots accepted")
	}
}
