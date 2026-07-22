package deployment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	c.Panel.InstallCommand = []string{"/app/panel", "install"}
	c.Limits.MaximumConcurrentOperations = 1
	repo := fakes.NewStateRepository()
	docker := &fakes.DockerClient{}
	health := &fakes.HealthChecker{}
	pg := &fakes.PostgresProvisioner{}
	ids := &fakes.IDGenerator{Value: "123e4567-e89b-42d3-a456-426614174099"}
	clock := &fakes.Clock{Time: time.Now().UTC()}
	reg := prometheus.NewRegistry()
	m := ccmetrics.New(reg)
	resourceFake := &fakes.ResourceCollector{Value: contracts.ResourceResponse{MemoryAvailableBytes: 1 << 30}}
	svc := New(c, repo, docker, pg, health, secrets, &fakes.BackupManager{}, &fakes.DeploymentStorage{Root: dir}, resourceFake, ids, clock, slog.New(slog.NewTextHandler(os.Stderr, nil)), m)
	req := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:1.0.0", Environment: map[string]string{}, Database: contracts.Database{DatabaseName: "panel_abcd_db", Username: "panel_abcd_user"}, Healthcheck: contracts.Healthcheck{Path: "/up"}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
	req.Aliases = []string{"panel.example.com"}
	raw, _ := json.Marshal(req)
	idempotencyKey := "123e4567-e89b-42d3-a456-426614174010"
	created, submitErr := svc.SubmitCreate(context.Background(), req, idempotencyKey, "POST", "/v1/deployments", raw)
	if submitErr != nil {
		t.Fatal(submitErr)
	}
	var accepted contracts.AcceptedCreateOperation
	if e = json.Unmarshal(created, &accepted); e != nil || len(accepted.Aliases) != 1 || accepted.Aliases[0] != "panel.example.com" {
		t.Fatalf("create response does not contain aliases: %s err=%v", created, e)
	}
	replayed, replayErr := svc.SubmitCreate(context.Background(), req, idempotencyKey, "POST", "/v1/deployments", raw)
	if replayErr != nil || string(replayed) != string(created) {
		t.Fatalf("identical create was not replayed: %s err=%v", replayed, replayErr)
	}
	changed := req
	changed.Aliases = []string{"other.example.com"}
	changedRaw, _ := json.Marshal(changed)
	if _, conflictErr := svc.SubmitCreate(context.Background(), changed, idempotencyKey, "POST", "/v1/deployments", changedRaw); !errors.Is(conflictErr, domain.ErrConflict) {
		t.Fatalf("alias-changing replay did not conflict: %v", conflictErr)
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
			routerRule := docker.Spec.TraefikLabels["traefik.http.routers.cc123e4567e89b42d3a456426614174000.rule"]
			if routerRule != "Host(`example.cloud.centralcorp.fr`) || Host(`panel.example.com`)" {
				t.Fatalf("unexpected alias router rule: %q", routerRule)
			}
			wantEnvironment := map[string]string{
				"APP_ENV":           "production",
				"APP_URL":           "https://example.cloud.centralcorp.fr",
				"CENTRALPANEL_MODE": "centralcloud",
				"CLOUD_PROJECT_ID":  req.ProjectID,
				"PANEL_MANAGED":     "true",
			}
			for key, want := range wantEnvironment {
				if got := docker.Spec.Environment[key]; got != want {
					t.Fatalf("environment %s=%q, want %q", key, got, want)
				}
			}
			if health.N != 2 {
				t.Fatalf("health checks=%d, want readiness before and health after installation", health.N)
			}
			if len(docker.ExecCommands) != 1 || strings.Join(docker.ExecCommands[0], " ") != strings.Join(c.Panel.InstallCommand, " ") {
				t.Fatalf("unexpected install command: %#v", docker.ExecCommands)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	t.Fatal("deployment did not become active")
}

func TestAcceptedCreateAlwaysReturnsAliasesArray(t *testing.T) {
	svc := &Service{}
	operation := domain.Operation{ID: "123e4567-e89b-42d3-a456-426614174099", DeploymentID: "123e4567-e89b-42d3-a456-426614174000"}
	for _, aliases := range [][]string{nil, {}} {
		body, err := svc.acceptedCreate(operation, aliases)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err = json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		got, ok := response["aliases"].([]any)
		if !ok || len(got) != 0 {
			t.Fatalf("create aliases missing or null: %s", body)
		}
	}
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
	bootstrap := contracts.Bootstrap{ //nolint:gosec // Deliberately recognizable test-only credentials verify secret separation.
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

func lifecycleService(t *testing.T, health domain.HealthChecker) (*Service, *fakes.StateRepository, *fakes.DockerClient, *fakes.PostgresProvisioner, *fakes.BackupManager, *fakes.DeploymentStorage, domain.Deployment) {
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
	r := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174020", ProjectID: "123e4567-e89b-42d3-a456-426614174021", Hostname: "lifecycle.cloud.centralcorp.fr", Aliases: []string{"lifecycle.example.com"}, Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:1.0.0", Resources: contracts.Resources{MemoryBytes: 128 << 20, CPULimit: .25}, Database: contracts.Database{DatabaseName: "panel_lifecycle_db", Username: "panel_lifecycle_user"}, Healthcheck: contracts.Healthcheck{Path: "/up", TimeoutSeconds: 5}}
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
	localData := &fakes.DeploymentStorage{Root: dir}
	svc := New(c, repo, docker, pg, health, secrets, backups, localData, resources, &fakes.IDGenerator{Value: "123e4567-e89b-42d3-a456-426614174099"}, &fakes.Clock{Time: time.Now().UTC()}, slog.New(slog.NewTextHandler(os.Stderr, nil)), m)
	return svc, repo, docker, pg, backups, localData, d
}

func TestUpgradeRollsBackImageAndDatabaseOnHealthFailure(t *testing.T) {
	health := &fakes.SequenceHealthChecker{Errors: []error{errors.New("new image unhealthy"), nil}}
	svc, repo, docker, _, backups, localData, d := lifecycleService(t, health)
	payload, _ := json.Marshal(contracts.UpgradeRequest{Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:2.0.0"})
	err := svc.upgrade(context.Background(), domain.Operation{ID: "upgrade", DeploymentID: d.Request.DeploymentID, Type: OpUpgrade, Payload: payload})
	var rolledBack *rollbackError
	if !errors.As(err, &rolledBack) {
		t.Fatalf("expected rollback error, got %v", err)
	}
	got, _ := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if got.State != domain.StateActive || got.Request.Image != d.Request.Image || !backups.Created || !backups.Restored {
		t.Fatalf("rollback incomplete: state=%s image=%s backups=%+v", got.State, got.Request.Image, backups)
	}
	if len(got.Request.Aliases) != 1 || got.Request.Aliases[0] != d.Request.Aliases[0] || !strings.Contains(docker.Spec.TraefikLabels["traefik.http.routers.cc123e4567e89b42d3a456426614174020.rule"], "Host(`lifecycle.example.com`)") {
		t.Fatalf("upgrade did not preserve alias: deployment=%#v labels=%#v", got.Request.Aliases, docker.Spec.TraefikLabels)
	}
	if localData.PanelPurged {
		t.Fatal("upgrade purged persistent panel storage")
	}
	if len(docker.ExecCommands) != 1 || strings.Join(docker.ExecCommands[0], " ") != "php artisan migrate --force --no-interaction" {
		t.Fatalf("unexpected upgrade migration command: %#v", docker.ExecCommands)
	}
}

func TestSubmitUpgradeAppliesDigestPolicy(t *testing.T) {
	svc, _, _, _, _, _, d := lifecycleService(t, &fakes.HealthChecker{})
	svc.cfg.Docker.RequireImageDigest = true
	tagged := contracts.UpgradeRequest{Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:2.0.0"}
	raw, _ := json.Marshal(tagged)
	if _, err := svc.SubmitUpgrade(context.Background(), d.Request.DeploymentID, "123e4567-e89b-42d3-a456-426614174051", "POST", "/upgrade", tagged, raw); err == nil {
		t.Fatal("mutable upgrade tag accepted while digest policy enabled")
	}
	digested := contracts.UpgradeRequest{Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud@sha256:" + strings.Repeat("a", 64)}
	raw, _ = json.Marshal(digested)
	if _, err := svc.SubmitUpgrade(context.Background(), d.Request.DeploymentID, "123e4567-e89b-42d3-a456-426614174052", "POST", "/upgrade", digested, raw); err != nil {
		t.Fatalf("digest upgrade rejected: %v", err)
	}
}

func TestSoftDeletePreservesDatabaseAndPurgeRemovesIt(t *testing.T) {
	svc, repo, _, pg, _, localData, d := lifecycleService(t, &fakes.HealthChecker{})
	if err := svc.remove(context.Background(), domain.Operation{ID: "soft", DeploymentID: d.Request.DeploymentID}, false); err != nil {
		t.Fatal(err)
	}
	soft, _ := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if soft.State != domain.StateDeleted || soft.CredentialsRef == "" || pg.Dropped {
		t.Fatalf("soft delete lost data: %+v", soft)
	}
	if len(soft.Request.Aliases) != 1 || soft.Request.Aliases[0] != d.Request.Aliases[0] {
		t.Fatalf("soft delete changed aliases: %#v", soft.Request.Aliases)
	}
	if localData.PanelPurged {
		t.Fatal("soft delete purged persistent storage")
	}
	soft.State = domain.StateActive
	if err := repo.SaveDeployment(context.Background(), soft); err != nil {
		t.Fatal(err)
	}
	if err := svc.remove(context.Background(), domain.Operation{ID: "purge", DeploymentID: d.Request.DeploymentID}, true); err != nil {
		t.Fatal(err)
	}
	if !pg.Dropped {
		t.Fatal("PostgreSQL material was not purged")
	}
	if !localData.PanelPurged {
		t.Fatal("purge did not delete persistent storage")
	}
	if err := svc.remove(context.Background(), domain.Operation{ID: "purge-replay", DeploymentID: d.Request.DeploymentID}, true); err != nil {
		t.Fatalf("partially completed purge was not replayable: %v", err)
	}
	if err := repo.CompletePurge(context.Background(), "purge", d.Request.DeploymentID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetDeployment(context.Background(), d.Request.DeploymentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("purged deployment remains in state: %v", err)
	}
}

func TestStopAndStartPreservePersistentStorage(t *testing.T) {
	svc, repo, _, _, _, localData, d := lifecycleService(t, &fakes.HealthChecker{})
	if err := svc.stop(context.Background(), domain.Operation{ID: "stop", DeploymentID: d.Request.DeploymentID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.start(context.Background(), domain.Operation{ID: "start", DeploymentID: d.Request.DeploymentID}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if err != nil || got.State != domain.StateActive {
		t.Fatalf("state=%s err=%v", got.State, err)
	}
	if len(got.Request.Aliases) != 1 || got.Request.Aliases[0] != d.Request.Aliases[0] {
		t.Fatalf("stop/start changed aliases: %#v", got.Request.Aliases)
	}
	if err = svc.execute(context.Background(), domain.Operation{ID: "restart", DeploymentID: d.Request.DeploymentID, Type: OpRestart}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetDeployment(context.Background(), d.Request.DeploymentID)
	if err != nil || len(got.Request.Aliases) != 1 || got.Request.Aliases[0] != d.Request.Aliases[0] {
		t.Fatalf("restart changed aliases: %#v err=%v", got.Request.Aliases, err)
	}
	if localData.PanelPurged {
		t.Fatal("stop/start purged persistent storage")
	}
}

func TestAdminResetPayloadIsEncryptedAndExecuted(t *testing.T) {
	svc, repo, docker, _, _, _, d := lifecycleService(t, &fakes.HealthChecker{})
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
	panelJSON, err := json.Marshal(panelAdminReset{Email: request.AdminEmail, Password: request.AdminPassword}) //nolint:gosec // Test-only serialization verifies the protected secret payload shape.
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
