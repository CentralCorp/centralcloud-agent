package docker

import (
	"strings"
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/docker/docker/api/types/network"
)

func TestNetworkAddressPrefersConfiguredNetwork(t *testing.T) {
	networks := map[string]*network.EndpointSettings{
		"centralcloud_frontend": {IPAddress: "172.20.0.10"},
		"centralcloud_egress":   {IPAddress: "172.21.0.10"},
	}

	if got := networkAddress(networks, "centralcloud_egress"); got != "172.21.0.10" {
		t.Fatalf("networkAddress() = %q, want private egress address", got)
	}
}

func TestDeploymentNetworkNamesAreDeterministicAndIsolated(t *testing.T) {
	a, err := NetworkNames("123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NetworkNames("123e4567-e89b-42d3-a456-426614174001")
	if err != nil {
		t.Fatal(err)
	}
	if a.Frontend == b.Frontend || a.Backend == b.Backend || a.Frontend == a.Backend {
		t.Fatalf("networks are not isolated: a=%+v b=%+v", a, b)
	}
	if a.Frontend != "centralcloud-fe-123e4567e89b42d3a456426614174000" {
		t.Fatalf("unexpected deterministic name: %s", a.Frontend)
	}
}

func TestValidateNetworkRequiresExactOwnership(t *testing.T) {
	id := "123e4567-e89b-42d3-a456-426614174000"
	names, _ := NetworkNames(id)
	n := network.Inspect{Name: names.Frontend, Driver: "bridge", Internal: false, Labels: map[string]string{"centralcloud.managed": "true", "centralcloud.deployment_id": id, "centralcloud.network_role": "frontend"}}
	if err := validateNetwork(n, names.Frontend, false, id, "frontend"); err != nil {
		t.Fatal(err)
	}
	n.Labels["centralcloud.deployment_id"] = "123e4567-e89b-42d3-a456-426614174099"
	if err := validateNetwork(n, names.Frontend, false, id, "frontend"); err == nil {
		t.Fatal("foreign network ownership accepted")
	}
}

func TestNetworkAddressFallbackIsDeterministic(t *testing.T) {
	networks := map[string]*network.EndpointSettings{
		"z-network": {IPAddress: "172.20.0.20"},
		"a-network": {IPAddress: "172.20.0.10"},
	}

	if got := networkAddress(networks, "missing"); got != "172.20.0.10" {
		t.Fatalf("networkAddress() = %q, want address from alphabetically first network", got)
	}
}

func TestContainerTmpfsIsWritableByNonRootPanel(t *testing.T) {
	spec := domain.ContainerSpec{
		Deployment: contracts.CreateDeploymentRequest{Resources: contracts.Resources{MemoryBytes: 64 << 20, CPULimit: 0.25}},
		PidsLimit:  128,
	}
	host := containerHostConfig(spec, []string{"/host/storage:/app/storage"})
	for _, path := range []string{"/tmp", "/run"} {
		options := host.Tmpfs[path]
		if !strings.Contains(options, "mode=1777") {
			t.Fatalf("tmpfs %s must be writable by the non-root panel user: %q", path, options)
		}
	}
}
