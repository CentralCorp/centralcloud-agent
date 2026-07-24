package backup

import (
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
)

func TestBackupContainerUsesTheManagedPanelIdentity(t *testing.T) {
	cfg := config.Defaults()
	cfg.Docker.PanelUser = "999:987"

	containerConfig := backupContainerConfig(cfg, "deployment-id", []string{"pg_dump"})

	if containerConfig.User != "999:987" {
		t.Fatalf("backup container user = %q", containerConfig.User)
	}
	if containerConfig.Image != cfg.Postgres.BackupImage {
		t.Fatalf("backup image = %q", containerConfig.Image)
	}
	if containerConfig.Labels["centralcloud.deployment_id"] != "deployment-id" {
		t.Fatalf("backup deployment ownership is missing: %#v", containerConfig.Labels)
	}
}
