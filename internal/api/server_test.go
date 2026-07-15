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
	"github.com/centralcorp/centralcloud-node-agent/internal/fakes"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func testServer(t *testing.T, mode string) *Server {
	t.Helper()
	c := config.Defaults()
	c.Security.Mode = mode
	c.Security.AllowedClientSANs = []string{"control-plane.internal"}
	if mode == "token" {
		c.Security.TokenFile = filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(c.Security.TokenFile, []byte(strings.Repeat("t", 32)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := fakes.NewStateRepository()
	docker := &fakes.DockerClient{}
	pg := &fakes.PostgresProvisioner{}
	resources := &fakes.ResourceCollector{}
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
