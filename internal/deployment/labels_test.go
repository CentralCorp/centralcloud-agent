package deployment

import (
	"strings"
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

func TestLabels(t *testing.T) {
	r := contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:1.2.3"}
	m := ManagementLabels(r)
	if m["centralcloud.version"] != "1.2.3" || m["centralcloud.deployment_id"] != r.DeploymentID {
		t.Fatal(m)
	}
	c := config.Defaults()
	frontend := "centralcloud-fe-123e4567e89b42d3a456426614174000"
	tr := TraefikLabels(r, c, frontend)
	if tr["traefik.enable"] != "true" || tr["traefik.docker.network"] != frontend {
		t.Fatal(tr)
	}
	router := "traefik.http.routers.cc123e4567e89b42d3a456426614174000"
	if tr[router+".rule"] != "Host(`example.cloud.centralcorp.fr`)" {
		t.Fatalf("alias-free router behavior changed: %#v", tr)
	}
}

func TestTraefikLabelsIncludeAliasAndCertificateResolver(t *testing.T) {
	r := contracts.CreateDeploymentRequest{
		DeploymentID: "123e4567-e89b-42d3-a456-426614174000",
		Hostname:     "panel.cloud.centralcorp.fr",
		Aliases:      []string{"panel.example.com"},
	}
	c := config.Defaults()
	labels, err := validatedTraefikLabels(r, c, "frontend")
	if err != nil {
		t.Fatal(err)
	}
	router := "traefik.http.routers.cc123e4567e89b42d3a456426614174000"
	if got, want := labels[router+".rule"], "Host(`panel.cloud.centralcorp.fr`) || Host(`panel.example.com`)"; got != want {
		t.Fatalf("router rule=%q want=%q", got, want)
	}
	if labels[router+".entrypoints"] != c.Traefik.Entrypoint || labels[router+".tls"] != "true" || labels[router+".tls.certresolver"] != c.Traefik.CertificateResolver {
		t.Fatalf("TLS labels are incomplete: %#v", labels)
	}

	c.Traefik.CertificateResolver = ""
	labels, err = validatedTraefikLabels(r, c, "frontend")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := labels[router+".tls"]; exists {
		t.Fatalf("TLS label added without resolver: %#v", labels)
	}
	if _, exists := labels[router+".tls.certresolver"]; exists {
		t.Fatalf("certificate resolver label added without resolver: %#v", labels)
	}

	r.Aliases = []string{"evil.example.com`) || PathPrefix(`/"}
	if _, err = validatedTraefikLabels(r, c, "frontend"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unsafe router host accepted: %v", err)
	}
}
