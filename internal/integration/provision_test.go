//go:build integration

package integration

import (
	"context"
	"net"
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
	"github.com/docker/docker/api/types/network"
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
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	c.Traefik.ContainerName = "centralcloud-traefik"
	d, err := ccdocker.New(c.Docker.Socket, "", "", c.Traefik.ContainerName)
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
	networks, err := d.EnsureDeploymentNetworks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer d.RemoveDeploymentNetworks(context.Background(), id)
	secret := filepath.Join(t.TempDir(), "postgres_password")
	if err = os.WriteFile(secret, []byte(password), 0400); err != nil {
		t.Fatal(err)
	}
	r := contracts.CreateDeploymentRequest{DeploymentID: id, ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "integration.cloud.centralcorp.fr", Aliases: []string{"integration.example.com"}, Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:integration", Resources: contracts.Resources{MemoryBytes: 128 << 20, CPULimit: .25}, Database: contracts.Database{DatabaseName: db, Username: user}, Healthcheck: contracts.Healthcheck{Path: "/up", TimeoutSeconds: 30}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
	traefikLabels := deployment.TraefikLabels(r, c, networks.Frontend)
	spec := domain.ContainerSpec{Deployment: r, Environment: map[string]string{"PGPASSWORD_FILE": "/run/secrets/postgres_password"}, SecretFiles: map[string]string{"postgres_password": secret}, StorageDirectory: t.TempDir(), ManagementLabels: deployment.ManagementLabels(r), TraefikLabels: traefikLabels, FrontendNetwork: networks.Frontend, BackendNetwork: networks.Backend, User: "65532:65532", PidsLimit: 64}
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
	if got := inspected.Config.Labels["traefik.http.routers.cc123e4567e89b42d3a456426614174099.rule"]; got != "Host(`integration.cloud.centralcorp.fr`) || Host(`integration.example.com`)" {
		t.Fatalf("unexpected alias router rule: %q", got)
	}
	frontend, err := raw.NetworkInspect(ctx, networks.Frontend, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := raw.NetworkInspect(ctx, networks.Backend, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, inspectedNetwork := range []network.Inspect{frontend, backend} {
		if inspectedNetwork.Labels["centralcloud.managed"] != "true" || inspectedNetwork.Labels["centralcloud.deployment_id"] != id {
			t.Fatalf("missing network ownership labels: %#v", inspectedNetwork.Labels)
		}
	}
	secondID := "123e4567-e89b-42d3-a456-426614174098"
	secondNetworks, err := d.EnsureDeploymentNetworks(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	defer d.RemoveDeploymentNetworks(context.Background(), secondID)
	secondRequest := r
	secondRequest.DeploymentID = secondID
	secondRequest.ProjectID = "123e4567-e89b-42d3-a456-426614174097"
	secondRequest.Hostname = "integration-second.cloud.centralcorp.fr"
	secondRequest.Aliases = nil
	secondTraefikLabels := deployment.TraefikLabels(secondRequest, c, secondNetworks.Frontend)
	secondSpec := domain.ContainerSpec{Deployment: secondRequest, Environment: map[string]string{"PGPASSWORD_FILE": "/run/secrets/postgres_password"}, SecretFiles: map[string]string{"postgres_password": secret}, StorageDirectory: t.TempDir(), ManagementLabels: deployment.ManagementLabels(secondRequest), TraefikLabels: secondTraefikLabels, FrontendNetwork: secondNetworks.Frontend, BackendNetwork: secondNetworks.Backend, User: "65532:65532", PidsLimit: 64}
	if _, err = d.CreateContainer(ctx, secondSpec); err != nil {
		t.Fatal(err)
	}
	defer d.RemoveContainer(context.Background(), secondID)
	if err = d.StartContainer(ctx, secondID); err != nil {
		t.Fatal(err)
	}
	secondInfo, err := d.InspectDeployment(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if networks.Frontend == secondNetworks.Frontend || networks.Backend == secondNetworks.Backend {
		t.Fatal("two deployments share a managed network")
	}
	if err = d.Exec(ctx, id, []string{"/fake-panel", "tcpcheck", net.JoinHostPort(secondInfo.Address, "8080")}); err == nil {
		t.Fatal("panel A could contact panel B directly across isolated backend networks")
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
