package domain

import (
	"strings"
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

func validRequest() contracts.CreateDeploymentRequest {
	return contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", ProjectID: "123e4567-e89b-42d3-a456-426614174001", Hostname: "example.cloud.centralcorp.fr", Image: "ghcr.io/centralcorp-cloud/centralpanel-cloud:1.0.0", Environment: map[string]string{}, Database: contracts.Database{DatabaseName: "panel_abcd_db", Username: "panel_abcd_user"}, Healthcheck: contracts.Healthcheck{Path: "/up"}, Bootstrap: contracts.Bootstrap{AdminName: "Owner", AdminEmail: "owner@example.test", AdminPassword: "long-bootstrap-password", InternalSecret: "12345678901234567890123456789012"}}
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
	if r.Aliases == nil || len(r.Aliases) != 0 {
		t.Fatalf("missing aliases were not normalized to an empty list: %#v", r.Aliases)
	}
	r = validRequest()
	r.Environment["PGPASSWORD"] = "leak"
	if ValidateCreate(&r, c) == nil {
		t.Fatal("reserved secret variable accepted")
	}
}

func TestValidateCreateAliases(t *testing.T) {
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	r := validRequest()
	r.Aliases = []string{"Panel.Example.COM."}
	if err := ValidateCreate(&r, c); err != nil {
		t.Fatalf("valid alias rejected: %v", err)
	}
	if len(r.Aliases) != 1 || r.Aliases[0] != "panel.example.com" {
		t.Fatalf("alias was not normalized: %#v", r.Aliases)
	}

	longDomain := strings.Join([]string{strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 63)}, ".")
	invalid := map[string][]string{
		"URL":                {"https://panel.example.com"},
		"IPv4":               {"192.0.2.1"},
		"IPv6":               {"2001:db8::1"},
		"wildcard":           {"*.example.com"},
		"port":               {"panel.example.com:443"},
		"userinfo":           {"user@panel.example.com"},
		"path":               {"panel.example.com/path"},
		"query":              {"panel.example.com?x=1"},
		"fragment":           {"panel.example.com#fragment"},
		"long label":         {strings.Repeat("a", 64) + ".example.com"},
		"long domain":        {longDomain},
		"Unicode":            {"panél.example.com"},
		"canonical hostname": {"example.cloud.centralcorp.fr"},
		"more than one":      {"one.example.com", "two.example.com"},
		"duplicates":         {"same.example.com", "SAME.EXAMPLE.COM."},
	}
	for name, aliases := range invalid {
		t.Run(name, func(t *testing.T) {
			request := validRequest()
			request.Aliases = aliases
			if err := ValidateCreate(&request, c); err == nil {
				t.Fatalf("invalid aliases accepted: %#v", aliases)
			}
		})
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

func TestEnvironmentAllowlistAndSecretKeys(t *testing.T) {
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	for _, key := range []string{"UNKNOWN", "API_TOKEN", "APP_KEY", "PGPASSWORD", "APP_ENV", "APP_URL", "CENTRALPANEL_MODE", "CLOUD_PROJECT_ID"} {
		r := validRequest()
		r.Environment = map[string]string{key: "must-not-appear-in-error"}
		err := ValidateCreate(&r, c)
		if err == nil {
			t.Fatalf("environment key %q accepted", key)
		}
		if strings.Contains(err.Error(), "must-not-appear-in-error") {
			t.Fatalf("environment value leaked for %q: %v", key, err)
		}
	}
	c.Panel.AllowedEnvironmentKeys = []string{"FEATURE_FLAG"}
	r := validRequest()
	r.Environment = map[string]string{"FEATURE_FLAG": "enabled"}
	if err := ValidateCreate(&r, c); err != nil {
		t.Fatal(err)
	}
}

func TestPanelImageDigestPolicy(t *testing.T) {
	c := config.Defaults()
	c.Traefik.DomainSuffix = "cloud.centralcorp.fr"
	tagged := validRequest()
	if err := ValidateCreate(&tagged, c); err != nil {
		t.Fatalf("tag rejected while digest policy disabled: %v", err)
	}
	c.Docker.RequireImageDigest = true
	if err := ValidateCreate(&tagged, c); err == nil {
		t.Fatal("mutable tag accepted while digest policy enabled")
	}
	digested := validRequest()
	digested.Image = "ghcr.io/centralcorp-cloud/centralpanel-cloud@sha256:" + strings.Repeat("a", 64)
	if err := ValidateCreate(&digested, c); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	digested.Image = "ghcr.io/centralcorp/other@sha256:" + strings.Repeat("a", 64)
	if err := ValidateCreate(&digested, c); err == nil {
		t.Fatal("digest from another repository accepted")
	}
}
