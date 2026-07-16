//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/deployment"
	ccdocker "github.com/centralcorp/centralcloud-node-agent/internal/docker"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/postgres"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/docker/docker/client"
)

func TestIsolatedDockerAndPostgresLifecycle(t *testing.T) {
	if os.Getenv("CENTRALCLOUD_INTEGRATION") != "1" {
		t.Skip("set CENTRALCLOUD_INTEGRATION=1 only in an isolated Docker environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := config.Defaults()
	c.Postgres.Host = "127.0.0.1"
	c.Postgres.AdministratorUsername = "centralcloud_provisioner"
	c.Postgres.AdministratorPasswordFile = os.Getenv("CENTRALCLOUD_POSTGRES_PASSWORD_FILE")
	c.Docker.FrontendNetwork = "centralcloud_it_frontend"
	c.Docker.EgressNetwork = "centralcloud_it_egress"
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	d, err := ccdocker.New(c.Docker.Socket, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pg, err := postgres.New(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	id := "123e4567-e89b-42d3-a456-426614174099"
	db, user, password := "panel_integration_db", "panel_integration_user", "integration-password"
	defer pg.DropRoleAndDatabase(context.Background(), db, user, id)
	if err = pg.EnsureRoleAndDatabase(ctx, db, user, password, id); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureNetwork(ctx, c.Docker.FrontendNetwork, false); err != nil {
		t.Fatal(err)
	}
	if err = d.EnsureNetwork(ctx, c.Docker.EgressNetwork, true); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "postgres_password")
	if err = os.WriteFile(secret, []byte(password), 0400); err != nil {
		t.Fatal(err)
	}
	r := contracts.CreateDeploymentRequest{DeploymentID: id, ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "integration.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp/centralpanel:integration", Resources: contracts.Resources{MemoryBytes: 128 << 20, CPULimit: .25}, Database: contracts.Database{DatabaseName: db, Username: user}, Healthcheck: contracts.Healthcheck{Path: "/health", TimeoutSeconds: 30}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
	spec := domain.ContainerSpec{Deployment: r, Environment: map[string]string{"PGPASSWORD_FILE": "/run/secrets/postgres_password"}, SecretFiles: map[string]string{"postgres_password": secret}, StorageDirectory: t.TempDir(), ManagementLabels: deployment.ManagementLabels(r), TraefikLabels: deployment.TraefikLabels(r, c), FrontendNetwork: c.Docker.FrontendNetwork, EgressNetwork: c.Docker.EgressNetwork, User: "65532:65532", PidsLimit: 64}
	containerID, err := d.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer d.RemoveContainer(context.Background(), id)
	if err = d.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	for {
		info, inspectErr := d.InspectDeployment(ctx, id)
		if inspectErr == nil && info.Health == "healthy" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container did not become healthy: %v", inspectErr)
		}
		time.Sleep(time.Second)
	}
	raw, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	inspected, err := raw.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Config.Labels["centralcloud.deployment_id"] != id || inspected.Config.Labels["traefik.enable"] != "true" {
		t.Fatalf("missing labels: %#v", inspected.Config.Labels)
	}
	if len(inspected.HostConfig.PortBindings) != 0 || inspected.HostConfig.Memory != 128<<20 || inspected.HostConfig.NanoCPUs != 250000000 {
		t.Fatal("container exposure or limits are incorrect")
	}
	if err = d.StopContainer(ctx, id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err = d.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = d.RemoveContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ContainerInspect(ctx, containerID); !client.IsErrNotFound(err) {
		t.Fatalf("container still exists: %v", err)
	}
}
