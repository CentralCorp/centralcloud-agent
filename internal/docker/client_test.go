package docker

import (
	"testing"

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

func TestNetworkAddressFallbackIsDeterministic(t *testing.T) {
	networks := map[string]*network.EndpointSettings{
		"z-network": {IPAddress: "172.20.0.20"},
		"a-network": {IPAddress: "172.20.0.10"},
	}

	if got := networkAddress(networks, "missing"); got != "172.20.0.10" {
		t.Fatalf("networkAddress() = %q, want address from alphabetically first network", got)
	}
}
