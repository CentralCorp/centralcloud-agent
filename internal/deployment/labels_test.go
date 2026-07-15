package deployment

import (
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

func TestLabels(t *testing.T) {
	r := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp/centralpanel:1.2.3"}
	m := ManagementLabels(r)
	if m["centralcloud.version"] != "1.2.3" || m["centralcloud.deployment_id"] != r.DeploymentID {
		t.Fatal(m)
	}
	c := config.Defaults()
	tr := TraefikLabels(r, c)
	if tr["traefik.enable"] != "true" || tr["traefik.docker.network"] != c.Docker.FrontendNetwork {
		t.Fatal(tr)
	}
}
