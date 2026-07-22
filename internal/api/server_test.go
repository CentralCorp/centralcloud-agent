package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/fakes"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/prometheus/client_golang/prometheus"
)

func testServer(t *testing.T, mode string) *Server {
	t.Helper()
	c := config.Defaults()
	c.Security.Mode = mode
	c.Security.AllowedClientSANs = []string{"control-plane.internal"}
	c.Node.ID = "123e4567-e89b-42d3-a456-426614174000"
	c.Node.Name = "node-test-01"
	if mode == "token" {
		c.Security.TokenFile = filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(c.Security.TokenFile, []byte(strings.Repeat("t", 32)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return testServerConfig(t, c)
}

func testServerConfig(t *testing.T, c config.Config) *Server {
	t.Helper()
	repo := fakes.NewStateRepository()
	docker := &fakes.DockerClient{}
	pg := &fakes.PostgresProvisioner{}
	resources := &fakes.ResourceCollector{Value: contracts.ResourceResponse{NodeID: c.Node.ID}}
	m := ccmetrics.New(prometheus.NewRegistry())
	s, err := New(c, nil, repo, docker, pg, resources, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenAuthenticationAndJSONMethodError(t *testing.T) {
	h := testServer(t, "token").Handler()
	request := func(method string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), method, "/v1/health", nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := request(http.MethodGet, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", got.Code)
	}
	if got := request(http.MethodGet, true); got.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
	got := request(http.MethodPost, true)
	if got.Code != http.StatusMethodNotAllowed || got.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%s", got.Code, got.Header().Get("Content-Type"))
	}
	var response map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
}

func TestMTLSSANAllowlist(t *testing.T) {
	h := testServer(t, "mtls").Handler()
	for _, tc := range []struct {
		name string
		dns  string
		want int
	}{{"allowed", "control-plane.internal", 200}, {"denied", "other.internal", 401}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/health", nil)
			r.TLS = &tls.ConnectionState{}
			r.TLS.PeerCertificates = []*x509.Certificate{{DNSNames: []string{tc.dns}}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMutationRejectsMissingReplayHeaders(t *testing.T) {
	h := testServer(t, "token").Handler()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/deployments", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_headers") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHealthIncludesPersistentNodeIdentity(t *testing.T) {
	h := testServer(t, "token").Handler()
	for _, path := range []string{"/v1/health", "/v1/resources"} {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["node_id"] != "123e4567-e89b-42d3-a456-426614174000" {
			t.Fatalf("unexpected node identity for %s: %#v", path, response)
		}
		if path == "/v1/health" && (response["node_name"] != "node-test-01" || response["agent_version"] != response["version"]) {
			t.Fatalf("unexpected health identity: %#v", response)
		}
		if path == "/v1/health" {
			capabilities, ok := response["capabilities"].([]any)
			if !ok || len(capabilities) != 1 || capabilities[0] != "hostname_aliases" {
				t.Fatalf("unexpected health capabilities: %#v", response)
			}
		}
	}
}

func TestDeploymentResponsesAlwaysContainAliases(t *testing.T) {
	for _, aliases := range [][]string{nil, {"panel.example.com"}} {
		converted := convert(domain.Deployment{Request: contracts.CreateDeploymentRequest{Aliases: aliases}})
		body, err := json.Marshal(converted)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err = json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		got, ok := response["aliases"].([]any)
		if !ok || len(got) != len(aliases) {
			t.Fatalf("aliases missing or null: %s", body)
		}
	}
}

func TestAllowedSourceCIDRsUseRemoteAddrAndIgnoreForwardedFor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cidrs      []string
		remoteAddr string
		forwarded  string
		want       int
	}{
		{name: "ipv4 allowed despite forwarded value", cidrs: []string{"192.0.2.0/24"}, remoteAddr: "192.0.2.10:1234", forwarded: "203.0.113.10", want: 200},
		{name: "forwarded value cannot bypass denial", cidrs: []string{"192.0.2.0/24"}, remoteAddr: "203.0.113.10:1234", forwarded: "192.0.2.10", want: 403},
		{name: "ipv6 allowed", cidrs: []string{"2001:db8::/64"}, remoteAddr: "[2001:db8::10]:1234", want: 200},
		{name: "ipv6 denied", cidrs: []string{"2001:db8::/64"}, remoteAddr: "[2001:db9::10]:1234", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Defaults()
			c.Security.Mode = "token"
			c.Security.TokenFile = filepath.Join(t.TempDir(), "token")
			c.Security.AllowedSourceCIDRs = tc.cidrs
			if err := os.WriteFile(c.Security.TokenFile, []byte(strings.Repeat("t", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			h := testServerConfig(t, c).Handler()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/health", nil)
			r.RemoteAddr = tc.remoteAddr
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
