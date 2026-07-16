package domain

import (
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

func validRequest() contracts.CreateDeploymentRequest {
	return contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp/centralpanel:1.0.0", Environment: map[string]string{"APP_ENV": "production"}, Database: contracts.Database{DatabaseName: "panel_abcd_db", Username: "panel_abcd_user"}, Healthcheck: contracts.Healthcheck{Path: "/health"}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
}
func TestValidateCreate(t *testing.T) {
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	r := validRequest()
	if e := ValidateCreate(&r, c); e != nil {
		t.Fatal(e)
	}
	if r.Resources.MemoryBytes == 0 || r.Healthcheck.TimeoutSeconds != 60 {
		t.Fatal("defaults were not applied")
	}
	r = validRequest()
	r.Environment["PGPASSWORD"] = "leak"
	if ValidateCreate(&r, c) == nil {
		t.Fatal("reserved secret variable accepted")
	}
}
func TestValidateCreateAcceptsUUIDv7DeploymentID(t *testing.T) {
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	r := validRequest()
	r.DeploymentID = "019f6ca3-0596-7179-948f-81d0308be869"
	if e := ValidateCreate(&r, c); e != nil {
		t.Fatal(e)
	}
}
func TestDatabaseIdentifier(t *testing.T) {
	for _, v := range []string{"ok_db", "a", "panel_123"} {
		if e := ValidateDatabaseIdentifier(v); e != nil {
			t.Fatalf("%s: %v", v, e)
		}
	}
	for _, v := range []string{"Bad", "x;DROP DATABASE postgres", "_hidden"} {
		if ValidateDatabaseIdentifier(v) == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}
