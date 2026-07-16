package deployment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/auth"
	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/fakes"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSimulatedProvisioning(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if e := os.WriteFile(filepath.Join(dir, "key"), []byte(base64.StdEncoding.EncodeToString(key)), 0600); e != nil {
		t.Fatal(e)
	}
	secrets, e := auth.NewSecretStore(filepath.Join(dir, "key"), dir)
	if e != nil {
		t.Fatal(e)
	}
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	c.Postgres.Host = "postgres"
	c.Panel.MigrationCommand = []string{"/app/panel", "migrate"}
	c.Limits.MaximumConcurrentOperations = 1
	repo := fakes.NewStateRepository()
	docker := &fakes.DockerClient{}
	pg := &fakes.PostgresProvisioner{}
	ids := &fakes.IDGenerator{Value: "123e4567-e89b-42d3-a456-426614174099"}
	clock := &fakes.Clock{Time: time.Now().UTC()}
	reg := prometheus.NewRegistry()
	m := ccmetrics.New(reg)
	resourceFake := &fakes.ResourceCollector{Value: contracts.ResourceResponse{MemoryAvailableBytes: 1 << 30}}
	svc := New(c, repo, docker, pg, &fakes.HealthChecker{}, secrets, &fakes.BackupManager{}, resourceFake, ids, clock, slog.New(slog.NewTextHandler(os.Stderr, nil)), m)
	req := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp/centralpanel:1.0.0", Environment: map[string]string{"APP_ENV": "production"}, Database: contracts.Database{DatabaseName: "panel_abcd_db", Username: "panel_abcd_user"}, Healthcheck: contracts.Healthcheck{Path: "/health"}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
	raw, _ := json.Marshal(req)
	if _, e = svc.SubmitCreate(context.Background(), req, "123e4567-e89b-42d3-a456-426614174010", "POST", "/v1/deployments", raw); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := repo.GetDeployment(ctx, req.DeploymentID)
		if d.State == domain.StateActive {
			cancel()
			svc.Wait()
			if !pg.Created || !docker.Running {
				t.Fatal("external fakes were not called")
			}
			if docker.Spec.Environment["PGPASSWORD_FILE"] == "" || docker.Spec.SecretFiles["panel_bootstrap.json"] == "" || docker.Spec.TraefikLabels["traefik.enable"] != "true" {
				t.Fatal("container contract incomplete")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	t.Fatal("deployment did not become active")
}

func TestMaterializeSecretsAdaptsBootstrapForPanelInstaller(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	secrets, err := auth.NewSecretStore(key, dir)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := contracts.Bootstrap{
		AdminName:      "Panel Owner",
		AdminEmail:     "owner@example.test",
		AdminPassword:  "long-bootstrap-password",
		InternalSecret: "must-not-be-written-to-bootstrap-file",
	}
	raw, _ := json.Marshal(bootstrap)
	encrypted, err := secrets.Encrypt(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{secrets: secrets}
	deployment := domain.Deployment{
		Request:            contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000"},
		EncryptedBootstrap: encrypted,
	}
	files, err := svc.materializeSecrets(deployment, permanentSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := os.ReadFile(files["panel_bootstrap.json"])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err = json.Unmarshal(materialized, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"name": bootstrap.AdminName, "email": bootstrap.AdminEmail, "password": bootstrap.AdminPassword}
	if len(got) != len(want) {
		t.Fatalf("unexpected bootstrap fields: %#v", got)
	}
	for field, value := range want {
		if got[field] != value {
			t.Fatalf("bootstrap field %q = %q, want %q", field, got[field], value)
		}
	}
}

func lifecycleService(t *testing.T, health domain.HealthChecker) (*Service, *fakes.StateRepository, *fakes.DockerClient, *fakes.PostgresProvisioner, *fakes.BackupManager, domain.Deployment) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	secrets, err := auth.NewSecretStore(filepath.Join(dir, "key"), dir)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := secrets.Encrypt("database-password")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	c.Postgres.Host = "postgres"
	c.Panel.MigrationCommand = []string{"/app/panel", "migrate"}
	r := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174020", ProjectID: "123e4567-e89b-42d3-a456-426614174021", Hostname: "lifecycle.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp/centralpanel:1.0.0", Resources: contracts.Resources{MemoryBytes: 128 << 20, CPULimit: .25}, Database: contracts.Database{DatabaseName: "panel_lifecycle_db", Username: "panel_lifecycle_user"}, Healthcheck: contracts.Healthcheck{Path: "/health", TimeoutSeconds: 5}}
	d := domain.Deployment{Request: r, State: domain.StateActive, CredentialsRef: "cccred://deployment/" + r.DeploymentID + "/postgres", EncryptedSecret: encrypted}
	repo := fakes.NewStateRepository()
	if err = repo.CreateDeployment(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	docker := &fakes.DockerClient{Exists: true, Running: true}
	pg := &fakes.PostgresProvisioner{}
	backups := &fakes.BackupManager{}
	m := ccmetrics.New(prometheus.NewRegistry())
	resources := &fakes.ResourceCollector{Value: contracts.ResourceResponse{MemoryAvailableBytes: 1 << 30}}
	svc := New(c, repo, docker, pg, health, secrets, backups, resources, &fakes.IDGenerator{Value: "123e4567-e89b-42d3-a456-426614174099"}, &fakes.Clock{Time: time.Now().UTC()}, slog.New(slog.NewTextHandler(os.Stderr, nil)), m)
	return svc, repo, docker, pg, backups, d
}

func TestUpgradeRollsBackImageAndDatabaseOnHealthFailure(t *testing.T) {
	health := &fakes.SequenceHealthChecker{Errors: []error{errors.New("new image unhealthy"), nil}}
	svc, repo, _, _, backups, d := lifecycleService(t, health)
	payload, _ := json.Marshal(contracts.UpgradeRequest{Image: "ghcr.io/centralcorp/centralpanel:2.0.0"})
	err := svc.upgrade(context.Background(), domain.Operation{ID: "upgrade", DeploymentID: d.Request.DeploymentID, Type: OpUpgrade, Payload: payload})
	var rolledBack *rollbackError
	if !errors.As(err, &rolledBack) {
		t.Fatalf("expected rollback error, got %v", err)
	}
	got, _ := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if got.State != domain.StateActive || got.Request.Image != d.Request.Image || !backups.Created || !backups.Restored {
		t.Fatalf("rollback incomplete: state=%s image=%s backups=%+v", got.State, got.Request.Image, backups)
	}
}

func TestSoftDeletePreservesDatabaseAndPurgeRemovesIt(t *testing.T) {
	svc, repo, _, pg, _, d := lifecycleService(t, &fakes.HealthChecker{})
	if err := svc.remove(context.Background(), domain.Operation{ID: "soft", DeploymentID: d.Request.DeploymentID}, false); err != nil {
		t.Fatal(err)
	}
	soft, _ := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if soft.State != domain.StateDeleted || soft.CredentialsRef == "" || pg.Dropped {
		t.Fatalf("soft delete lost data: %+v", soft)
	}
	soft.State = domain.StateActive
	if err := repo.SaveDeployment(context.Background(), soft); err != nil {
		t.Fatal(err)
	}
	if err := svc.remove(context.Background(), domain.Operation{ID: "purge", DeploymentID: d.Request.DeploymentID}, true); err != nil {
		t.Fatal(err)
	}
	purged, _ := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if !pg.Dropped || purged.CredentialsRef != "" || len(purged.EncryptedSecret) != 0 {
		t.Fatalf("purge incomplete: %+v", purged)
	}
}

func TestAdminResetPayloadIsEncryptedAndExecuted(t *testing.T) {
	svc, repo, docker, _, _, d := lifecycleService(t, &fakes.HealthChecker{})
	req := contracts.AdminResetRequest{AdminEmail: "owner@example.test", AdminPassword: "rotated-admin-password"}
	raw, _ := json.Marshal(req)
	operationID := "123e4567-e89b-42d3-a456-426614174099"
	if _, err := svc.SubmitAdminReset(context.Background(), d.Request.DeploymentID, "123e4567-e89b-42d3-a456-426614174055", "POST", "/v1/deployments/"+d.Request.DeploymentID+"/admin-reset", req, raw); err != nil {
		t.Fatal(err)
	}
	queued, _ := repo.GetOperation(context.Background(), operationID)
	if string(queued.Payload) == string(raw) || len(queued.Payload) == 0 {
		t.Fatal("admin reset payload was persisted in plaintext")
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, _ := repo.GetOperation(ctx, operationID)
		if op.Status == "succeeded" {
			cancel()
			svc.Wait()
			for _, call := range docker.Calls {
				if call == "exec" {
					return
				}
			}
			t.Fatal("admin reset command was not executed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	t.Fatal("admin reset operation did not complete")
}

func TestPanelAdminResetUsesInstallerFieldNames(t *testing.T) {
	request := contracts.AdminResetRequest{AdminEmail: "owner@example.test", AdminPassword: "rotated-admin-password"}
	panelJSON, err := json.Marshal(panelAdminReset{Email: request.AdminEmail, Password: request.AdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err = json.Unmarshal(panelJSON, &got); err != nil {
		t.Fatal(err)
	}
	if got["email"] != request.AdminEmail || got["password"] != request.AdminPassword {
		t.Fatalf("unexpected admin reset contract: %#v", got)
	}
	if _, exists := got["admin_email"]; exists {
		t.Fatalf("agent API field leaked into panel reset contract: %#v", got)
	}
}
