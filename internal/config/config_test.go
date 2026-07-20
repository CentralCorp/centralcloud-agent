package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDurationsAndSnakeCaseFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
server:
  address: "127.0.0.1:9443"
  operation_timeout: 3m
security:
  mode: token
  token_file: /tmp/token
  master_key_file: /tmp/key
docker:
  socket: unix:///var/run/docker.sock
  panel_image_repository: example/panel
postgres:
  host: postgres
  administrator_username: provisioner
  administrator_password_file: /tmp/postgres
traefik:
  domain_suffix: example.test
panel:
  migration_command: ["/panel", "migrate"]
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.OperationTimeout != 3*time.Minute || c.Docker.PanelImageRepository != "example/panel" {
		t.Fatalf("unexpected config: %#v", c)
	}
}

func TestDefaultsMatchCentralPanelManagedContract(t *testing.T) {
	c := Defaults()
	if c.Docker.PanelImageRepository != "ghcr.io/centralcorp-cloud/centralpanel-cloud" {
		t.Fatalf("unexpected panel repository: %q", c.Docker.PanelImageRepository)
	}
	wantInstall := []string{"php", "artisan", "auto:install", "--bootstrap-file=/run/secrets/panel_bootstrap.json", "--no-interaction"}
	if len(c.Panel.InstallCommand) != len(wantInstall) {
		t.Fatalf("unexpected install command: %#v", c.Panel.InstallCommand)
	}
	for i, want := range wantInstall {
		if c.Panel.InstallCommand[i] != want {
			t.Fatalf("install command[%d]=%q, want %q", i, c.Panel.InstallCommand[i], want)
		}
	}
	wantMigration := []string{"php", "artisan", "migrate", "--force", "--no-interaction"}
	if len(c.Panel.MigrationCommand) != len(wantMigration) {
		t.Fatalf("unexpected migration command: %#v", c.Panel.MigrationCommand)
	}
	for i, want := range wantMigration {
		if c.Panel.MigrationCommand[i] != want {
			t.Fatalf("migration command[%d]=%q, want %q", i, c.Panel.MigrationCommand[i], want)
		}
	}
	if len(c.Panel.AllowedEnvironmentKeys) != 0 {
		t.Fatalf("custom environment allowlist should be empty by default: %#v", c.Panel.AllowedEnvironmentKeys)
	}
}

func TestValidateRejectsInvalidNodeCIDRAndEnvironmentAllowlist(t *testing.T) {
	c := Defaults()
	c.Security.Mode = "token"
	c.Security.TokenFile = "/tmp/token"
	c.Security.MasterKeyFile = "/tmp/key"
	c.Postgres.Host = "postgres"
	c.Postgres.AdministratorUsername = "provisioner"
	c.Postgres.AdministratorPasswordFile = "/tmp/postgres"
	c.Traefik.DomainSuffix = "example.test"
	c.Panel.MigrationCommand = []string{"migrate"}
	c.Node.ID = "not-a-uuid"
	c.Security.AllowedSourceCIDRs = []string{"192.0.2.0/24", "broken"}
	c.Panel.AllowedEnvironmentKeys = []string{"APP_ENV", "bad-key"}
	if err := c.Validate(); err == nil {
		t.Fatal("invalid node identity, CIDR and environment key accepted")
	}
}
