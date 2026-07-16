package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/deployment"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

type contextKey string

const correlationKey contextKey = "correlation_id"

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Server struct {
	cfg       config.Config
	service   *deployment.Service
	repo      domain.StateRepository
	docker    domain.DockerClient
	postgres  domain.PostgresProvisioner
	resources domain.ResourceCollector
	metrics   *ccmetrics.Metrics
	log       *slog.Logger
	token     []byte
	allowed   map[string]bool
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
}

func New(c config.Config, svc *deployment.Service, repo domain.StateRepository, d domain.DockerClient, p domain.PostgresProvisioner, r domain.ResourceCollector, m *ccmetrics.Metrics, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: c, service: svc, repo: repo, docker: d, postgres: p, resources: r, metrics: m, log: log, allowed: map[string]bool{}, limiters: map[string]*rate.Limiter{}}
	for _, v := range c.Security.AllowedClientSANs {
		s.allowed[v] = true
	}
	if c.Security.Mode == "token" {
		b, e := os.ReadFile(c.Security.TokenFile)
		if e != nil {
			return nil, e
		}
		s.token = []byte(strings.TrimSpace(string(b)))
		if len(s.token) < 32 {
			return nil, fmt.Errorf("development token must be at least 32 bytes")
		}
	}
	return s, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/resources", s.resource)
	mux.HandleFunc("GET /v1/deployments", s.list)
	mux.HandleFunc("POST /v1/deployments", s.create)
	mux.HandleFunc("GET /v1/deployments/{id}", s.get)
	mux.HandleFunc("POST /v1/deployments/{id}/start", s.action(deployment.OpStart))
	mux.HandleFunc("POST /v1/deployments/{id}/stop", s.action(deployment.OpStop))
	mux.HandleFunc("POST /v1/deployments/{id}/restart", s.action(deployment.OpRestart))
	mux.HandleFunc("POST /v1/deployments/{id}/upgrade", s.upgrade)
	mux.HandleFunc("POST /v1/deployments/{id}/admin-reset", s.adminReset)
	mux.HandleFunc("POST /v1/deployments/{id}/purge-token", s.purgeToken)
	mux.HandleFunc("DELETE /v1/deployments/{id}", s.delete)
	mux.HandleFunc("GET /v1/deployments/{id}/logs", s.logs)
	mux.HandleFunc("GET /v1/operations/{id}", s.operation)
	mux.Handle("GET /metrics", promhttp.Handler())
	for _, pattern := range []string{"/v1/health", "/v1/resources", "/v1/deployments", "/v1/deployments/{id}", "/v1/deployments/{id}/start", "/v1/deployments/{id}/stop", "/v1/deployments/{id}/restart", "/v1/deployments/{id}/upgrade", "/v1/deployments/{id}/admin-reset", "/v1/deployments/{id}/purge-token", "/v1/deployments/{id}/logs", "/v1/operations/{id}", "/metrics"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusNotFound, "not_found", "route not found", nil)
	})
	return s.recover(s.context(s.limit(s.authenticate(mux))))
}
func (s *Server) context(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := strings.ToLower(r.Header.Get("X-Correlation-ID"))
		if cid == "" {
			cid = domain.UUIDGenerator{}.New()
		}
		w.Header().Set("X-Correlation-ID", cid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, cid)))
	})
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := false
		if s.cfg.Security.Mode == "token" {
			v := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			ok = len(v) == len(s.token) && subtle.ConstantTimeCompare([]byte(v), s.token) == 1
		} else if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			cert := r.TLS.PeerCertificates[0]
			for _, v := range cert.DNSNames {
				ok = ok || s.allowed[v]
			}
			for _, v := range cert.URIs {
				ok = ok || s.allowed[v.String()]
			}
		}
		if !ok {
			s.writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication failed", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) identity(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		return r.TLS.PeerCertificates[0].Subject.String()
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
func (s *Server) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.identity(r)
		s.mu.Lock()
		lim := s.limiters[id]
		if lim == nil {
			lim = rate.NewLimiter(rate.Limit(s.cfg.Server.RatePerSecond), s.cfg.Server.RateBurst)
			s.limiters[id] = lim
		}
		ok := lim.Allow()
		s.mu.Unlock()
		if !ok {
			s.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("http panic", "panic", fmt.Sprint(v), "correlation_id", correlation(r))
				s.writeError(w, r, http.StatusInternalServerError, "internal_error", "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func correlation(r *http.Request) string {
	v, _ := r.Context().Value(correlationKey).(string)
	return v
}
func (s *Server) mutation(w http.ResponseWriter, r *http.Request) bool {
	key := strings.ToLower(r.Header.Get("Idempotency-Key"))
	cid := strings.ToLower(r.Header.Get("X-Correlation-ID"))
	ts, e := time.Parse(time.RFC3339, r.Header.Get("X-Request-Timestamp"))
	if !uuidRE.MatchString(key) || !uuidRE.MatchString(cid) {
		s.writeError(w, r, 400, "invalid_headers", "Idempotency-Key and X-Correlation-ID must be UUIDs", nil)
		return false
	}
	if e != nil || s.cfg.Security.TimestampSkew <= 0 || time.Since(ts).Abs() > s.cfg.Security.TimestampSkew {
		s.writeError(w, r, 400, "stale_request", "invalid or stale X-Request-Timestamp", nil)
		return false
	}
	return true
}
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, dst any) ([]byte, bool) {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		s.writeError(w, r, 415, "unsupported_media_type", "Content-Type must be application/json", nil)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Server.MaxRequestBytes)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		s.writeError(w, r, 413, "request_too_large", "request body is too large", nil)
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e = dec.Decode(dst); e != nil {
		s.writeError(w, r, 400, "invalid_json", "invalid JSON request", nil)
		return nil, false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		s.writeError(w, r, 400, "invalid_json", "request must contain one JSON value", nil)
		return nil, false
	}
	return b, true
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	resp := contracts.HealthResponse{Status: "ok", Version: Version, Docker: "ok", Postgres: "ok", Database: "ok"}
	status := 200
	if e := s.docker.Ping(r.Context()); e != nil {
		resp.Docker = "error"
		resp.Status = "degraded"
		status = 503
		s.metrics.DockerHealth.Set(0)
	} else {
		s.metrics.DockerHealth.Set(1)
	}
	if e := s.postgres.Ping(r.Context()); e != nil {
		resp.Postgres = "error"
		resp.Status = "degraded"
		status = 503
		s.metrics.PostgresHealth.Set(0)
	} else {
		s.metrics.PostgresHealth.Set(1)
	}
	if e := s.repo.Ping(r.Context()); e != nil {
		resp.Database = "error"
		resp.Status = "degraded"
		status = 503
	}
	s.write(w, status, resp)
}

var Version = "dev"

func (s *Server) resource(w http.ResponseWriter, r *http.Request) {
	v, e := s.resources.Collect(r.Context())
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.metrics.DeploymentsTotal.Set(float64(v.DeploymentCount))
	s.metrics.DeploymentsActive.Set(float64(v.ActiveDeploymentCount))
	s.metrics.AvailableMemory.Set(float64(v.MemoryAvailableBytes))
	s.metrics.AvailableDisk.Set(float64(v.DiskAvailableBytes))
	s.write(w, 200, v)
}
func convert(d domain.Deployment) contracts.Deployment {
	return contracts.Deployment{DeploymentID: d.Request.DeploymentID, ProjectID: d.Request.ProjectID, Hostname: d.Request.Hostname, Image: d.Request.Image, State: string(d.State), Resources: d.Request.Resources, Database: d.Request.Database, Healthcheck: d.Request.Healthcheck, CredentialsRef: d.CredentialsRef, FailedStep: d.FailedStep, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	ds, e := s.service.ListDeployments(r.Context())
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	out := make([]contracts.Deployment, 0, len(ds))
	for _, d := range ds {
		out = append(out, convert(d))
	}
	s.write(w, 200, map[string]any{"deployments": out})
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	d, e := s.service.GetDeployment(r.Context(), r.PathValue("id"))
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.write(w, 200, convert(d))
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	var req contracts.CreateDeploymentRequest
	raw, ok := s.readJSON(w, r, &req)
	if !ok {
		return
	}
	b, e := s.service.SubmitCreate(r.Context(), req, r.Header.Get("Idempotency-Key"), r.Method, r.URL.Path, raw)
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.writeRaw(w, 202, b)
}
func (s *Server) action(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.mutation(w, r) {
			return
		}
		b, e := s.service.Submit(r.Context(), typ, r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.Path, nil)
		if e != nil {
			s.handleError(w, r, e)
			return
		}
		s.writeRaw(w, 202, b)
	}
}
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	var req contracts.UpgradeRequest
	raw, ok := s.readJSON(w, r, &req)
	if !ok {
		return
	}
	b, e := s.service.Submit(r.Context(), deployment.OpUpgrade, r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.Path, raw)
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.writeRaw(w, 202, b)
}
func (s *Server) adminReset(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	var req contracts.AdminResetRequest
	raw, ok := s.readJSON(w, r, &req)
	if !ok {
		return
	}
	b, err := s.service.SubmitAdminReset(r.Context(), r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.Path, req, raw)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	s.writeRaw(w, 202, b)
}
func (s *Server) purgeToken(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	b, e := s.service.IssuePurgeToken(r.Context(), r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.Path)
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.writeRaw(w, 201, b)
}
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	mode := r.URL.Query().Get("mode")
	var b []byte
	var e error
	switch mode {
	case "soft":
		b, e = s.service.Submit(r.Context(), deployment.OpDeleteSoft, r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.RequestURI(), nil)
	case "purge":
		b, e = s.service.SubmitPurge(r.Context(), r.PathValue("id"), r.Header.Get("Idempotency-Key"), r.Method, r.URL.RequestURI(), r.Header.Get("X-Purge-Token"))
	default:
		s.writeError(w, r, 400, "invalid_mode", "mode must be soft or purge", nil)
		return
	}
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	s.writeRaw(w, 202, b)
}
func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	o, e := s.service.GetOperation(r.Context(), r.PathValue("id"))
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	out := contracts.Operation{ID: o.ID, DeploymentID: o.DeploymentID, Type: o.Type, Status: o.Status, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
	if len(o.Result) > 0 {
		_ = json.Unmarshal(o.Result, &out.Result)
	}
	if o.ErrorCode != "" {
		out.Error = &contracts.ErrorBody{Code: o.ErrorCode, Message: o.ErrorMessage}
	}
	s.write(w, 200, out)
}
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	if limit < 1 || limit > 1000 {
		s.writeError(w, r, 400, "invalid_limit", "limit must be between 1 and 1000", nil)
		return
	}
	var since time.Time
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		b, e := base64.RawURLEncoding.DecodeString(cur)
		if e != nil {
			s.writeError(w, r, 400, "invalid_cursor", "invalid cursor", nil)
			return
		}
		since, e = time.Parse(time.RFC3339Nano, string(b))
		if e != nil {
			s.writeError(w, r, 400, "invalid_cursor", "invalid cursor", nil)
			return
		}
	}
	lines, next, e := s.service.ReadLogs(r.Context(), r.PathValue("id"), since, limit)
	if e != nil {
		s.handleError(w, r, e)
		return
	}
	cursor := ""
	if !next.IsZero() {
		cursor = base64.RawURLEncoding.EncodeToString([]byte(next.Format(time.RFC3339Nano)))
	}
	s.write(w, 200, contracts.LogPage{Lines: lines, NextCursor: cursor})
}
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, e error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(e, domain.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(e, domain.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(e, domain.ErrCapacity):
		status, code = 409, "capacity_exceeded"
	case strings.Contains(e.Error(), "invalid") || strings.Contains(e.Error(), "must") || strings.Contains(e.Error(), "outside") || strings.Contains(e.Error(), "allowed"):
		status, code = 400, "invalid_request"
	}
	message := e.Error()
	if status == 500 {
		message = "internal server error"
		s.log.Error("request failed", "error", e, "correlation_id", correlation(r))
	}
	s.writeError(w, r, status, code, message, nil)
}
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	s.write(w, status, contracts.ErrorResponse{Error: contracts.ErrorBody{Code: code, Message: message, CorrelationID: correlation(r), Details: details}})
}
func (s *Server) write(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	s.writeRaw(w, status, b)
}
func (s *Server) writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n')) // #nosec G705 -- b is always a JSON document produced or persisted by this API.
}
