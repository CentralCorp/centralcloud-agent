package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/logging"
	ccmetrics "github.com/centralcorp/centralcloud-node-agent/internal/metrics"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

const (
	OpCreate      = "create"
	OpStart       = "start"
	OpStop        = "stop"
	OpRestart     = "restart"
	OpUpgrade     = "upgrade"
	OpDeleteSoft  = "delete_soft"
	OpDeletePurge = "delete_purge"
	OpAdminReset  = "admin_reset"
)

type Service struct {
	cfg       config.Config
	repo      domain.StateRepository
	docker    domain.DockerClient
	postgres  domain.PostgresProvisioner
	health    domain.HealthChecker
	secrets   domain.SecretStore
	backups   domain.BackupManager
	resources domain.ResourceCollector
	ids       domain.IDGenerator
	clock     domain.Clock
	log       *slog.Logger
	metrics   *ccmetrics.Metrics
	wake      chan struct{}
	wg        sync.WaitGroup
}

type permanentSecrets struct {
	DatabasePassword string `json:"database_password"`
	AppKey           string `json:"app_key"`
	InternalSecret   string `json:"internal_secret"`
}

func (s *Service) encryptPermanentSecrets(databasePassword string, bootstrap contracts.Bootstrap) ([]byte, error) {
	sum := sha256.Sum256([]byte(databasePassword + bootstrap.InternalSecret))
	b, err := json.Marshal(permanentSecrets{
		DatabasePassword: databasePassword,
		AppKey:           "base64:" + base64.StdEncoding.EncodeToString(sum[:]),
		InternalSecret:   bootstrap.InternalSecret,
	})
	if err != nil {
		return nil, err
	}
	return s.secrets.Encrypt(string(b))
}

func (s *Service) decryptPermanentSecrets(encrypted []byte) (permanentSecrets, error) {
	value, err := s.secrets.Decrypt(encrypted)
	if err != nil {
		return permanentSecrets{}, err
	}
	var bundle permanentSecrets
	if json.Unmarshal([]byte(value), &bundle) == nil && bundle.DatabasePassword != "" {
		return bundle, nil
	}
	// Compatibility with state created before secret bundles were introduced.
	return permanentSecrets{DatabasePassword: value}, nil
}

func New(c config.Config, r domain.StateRepository, d domain.DockerClient, p domain.PostgresProvisioner, h domain.HealthChecker, secrets domain.SecretStore, b domain.BackupManager, resources domain.ResourceCollector, ids domain.IDGenerator, clock domain.Clock, log *slog.Logger, m *ccmetrics.Metrics) *Service {
	return &Service{cfg: c, repo: r, docker: d, postgres: p, health: h, secrets: secrets, backups: b, resources: resources, ids: ids, clock: clock, log: log, metrics: m, wake: make(chan struct{}, 1)}
}

func hashRequest(method, path string, body []byte) string {
	sum := sha256.Sum256(append(append([]byte(method+"\n"+path+"\n"), body...), byte('\n')))
	return hex.EncodeToString(sum[:])
}
func (s *Service) existingIdempotency(ctx context.Context, key, hash string) ([]byte, bool, error) {
	b, h, ok, e := s.repo.GetIdempotency(ctx, key)
	if e != nil || !ok {
		return nil, false, e
	}
	if h != hash {
		return nil, false, domain.ErrConflict
	}
	return b, true, nil
}
func (s *Service) accepted(o domain.Operation) ([]byte, error) {
	return json.Marshal(contracts.AcceptedOperation{OperationID: o.ID, DeploymentID: o.DeploymentID, Status: "queued"})
}

func (s *Service) SubmitCreate(ctx context.Context, r contracts.CreateDeploymentRequest, key, method, path string, raw []byte) ([]byte, error) {
	key = strings.ToLower(key)
	if e := domain.ValidateCreate(&r, s.cfg); e != nil {
		return nil, e
	}
	hash := hashRequest(method, path, raw)
	if b, ok, e := s.existingIdempotency(ctx, key, hash); e != nil || ok {
		return b, e
	}
	d, e := s.repo.GetDeployment(ctx, r.DeploymentID)
	switch {
	case errors.Is(e, domain.ErrNotFound):
		n, _, e := s.repo.CountDeployments(ctx)
		if e != nil {
			return nil, e
		}
		if n >= s.cfg.Limits.MaximumDeployments {
			return nil, domain.ErrCapacity
		}
		available, e := s.resources.Collect(ctx)
		if e != nil {
			return nil, fmt.Errorf("collect node capacity: %w", e)
		}
		if available.MemoryAvailableBytes < r.Resources.MemoryBytes {
			return nil, domain.ErrCapacity
		}
		secret, e := s.secrets.Generate()
		if e != nil {
			return nil, e
		}
		enc, e := s.encryptPermanentSecrets(secret, r.Bootstrap)
		if e != nil {
			return nil, e
		}
		bootstrapJSON, e := json.Marshal(r.Bootstrap)
		if e != nil {
			return nil, e
		}
		bootstrapEncrypted, e := s.secrets.Encrypt(string(bootstrapJSON))
		if e != nil {
			return nil, e
		}
		r.Bootstrap.AdminPassword = ""
		r.Bootstrap.InternalSecret = ""
		d = domain.Deployment{Request: r, State: domain.StatePending, CredentialsRef: "cccred://deployment/" + r.DeploymentID + "/postgres", EncryptedSecret: enc, EncryptedBootstrap: bootstrapEncrypted, CreatedAt: s.clock.Now()}
		if e = s.repo.CreateDeployment(ctx, d); e != nil {
			return nil, e
		}
	case e != nil:
		return nil, e
	case d.State == domain.StateDeleted || d.State == domain.StateFailed:
		if len(d.EncryptedSecret) == 0 {
			secret, e := s.secrets.Generate()
			if e != nil {
				return nil, e
			}
			d.EncryptedSecret, e = s.encryptPermanentSecrets(secret, r.Bootstrap)
			if e != nil {
				return nil, e
			}
			d.CredentialsRef = "cccred://deployment/" + r.DeploymentID + "/postgres"
		}
		bootstrapJSON, marshalErr := json.Marshal(r.Bootstrap)
		if marshalErr != nil {
			return nil, marshalErr
		}
		d.EncryptedBootstrap, e = s.secrets.Encrypt(string(bootstrapJSON))
		if e != nil {
			return nil, e
		}
		r.Bootstrap.AdminPassword = ""
		r.Bootstrap.InternalSecret = ""
		d.Request = r
		d.State = domain.StatePending
		d.FailedStep = ""
		if e = s.repo.SaveDeployment(ctx, d); e != nil {
			return nil, e
		}
	default:
		return nil, domain.ErrConflict
	}
	// The raw body participates in the idempotency hash, but is never persisted because it contains bootstrap secrets.
	o := domain.Operation{ID: s.ids.New(), DeploymentID: r.DeploymentID, Type: OpCreate}
	if e = s.repo.CreateOperation(ctx, o); e != nil {
		return nil, e
	}
	response, e := s.accepted(o)
	if e == nil {
		e = s.repo.PutIdempotency(ctx, key, hash, response)
	}
	s.signal()
	return response, e
}

func (s *Service) Submit(ctx context.Context, typ, id, key, method, path string, payload []byte) ([]byte, error) {
	key = strings.ToLower(key)
	hash := hashRequest(method, path, payload)
	if b, ok, e := s.existingIdempotency(ctx, key, hash); e != nil || ok {
		return b, e
	}
	if _, e := s.repo.GetDeployment(ctx, id); e != nil {
		return nil, e
	}
	o := domain.Operation{ID: s.ids.New(), DeploymentID: id, Type: typ, Payload: payload}
	if e := s.repo.CreateOperation(ctx, o); e != nil {
		return nil, e
	}
	response, e := s.accepted(o)
	if e == nil {
		e = s.repo.PutIdempotency(ctx, key, hash, response)
	}
	s.signal()
	return response, e
}

func (s *Service) SubmitAdminReset(ctx context.Context, id, key, method, path string, request contracts.AdminResetRequest, raw []byte) ([]byte, error) {
	if !strings.Contains(request.AdminEmail, "@") || len(request.AdminPassword) < 12 || len(request.AdminPassword) > 4096 {
		return nil, fmt.Errorf("valid admin_email and a password of at least 12 characters are required")
	}
	hash := hashRequest(method, path, raw)
	if b, ok, err := s.existingIdempotency(ctx, strings.ToLower(key), hash); err != nil || ok {
		return b, err
	}
	if _, err := s.repo.GetDeployment(ctx, id); err != nil {
		return nil, err
	}
	encrypted, err := s.secrets.Encrypt(string(raw))
	if err != nil {
		return nil, err
	}
	o := domain.Operation{ID: s.ids.New(), DeploymentID: id, Type: OpAdminReset, Payload: encrypted}
	if err = s.repo.CreateOperation(ctx, o); err != nil {
		return nil, err
	}
	response, err := s.accepted(o)
	if err == nil {
		err = s.repo.PutIdempotency(ctx, strings.ToLower(key), hash, response)
	}
	s.signal()
	return response, err
}
func (s *Service) IssuePurgeToken(ctx context.Context, id, key, method, path string) ([]byte, error) {
	key = strings.ToLower(key)
	hash := hashRequest(method, path, nil)
	if b, ok, e := s.existingIdempotency(ctx, key, hash); e != nil || ok {
		return b, e
	}
	if _, e := s.repo.GetDeployment(ctx, id); e != nil {
		return nil, e
	}
	token, e := s.secrets.Generate()
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(token))
	expires := s.clock.Now().Add(5 * time.Minute)
	if e = s.repo.CreatePurgeToken(ctx, id, sum[:], expires); e != nil {
		return nil, e
	}
	response, e := json.Marshal(map[string]any{"purge_token": token, "expires_at": expires})
	if e == nil {
		e = s.repo.PutIdempotency(ctx, key, hash, response)
	}
	return response, e
}
func (s *Service) SubmitPurge(ctx context.Context, id, key, method, path, token string) ([]byte, error) {
	key = strings.ToLower(key)
	hash := hashRequest(method, path, nil)
	if b, ok, e := s.existingIdempotency(ctx, key, hash); e != nil || ok {
		return b, e
	}
	sum := sha256.Sum256([]byte(token))
	ok, e := s.repo.ConsumePurgeToken(ctx, id, sum[:], s.clock.Now())
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, fmt.Errorf("invalid, expired, or consumed purge token")
	}
	o := domain.Operation{ID: s.ids.New(), DeploymentID: id, Type: OpDeletePurge}
	if e = s.repo.CreateOperation(ctx, o); e != nil {
		return nil, e
	}
	response, e := s.accepted(o)
	if e == nil {
		e = s.repo.PutIdempotency(ctx, key, hash, response)
	}
	s.signal()
	return response, e
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	for i := 0; i < s.cfg.Limits.MaximumConcurrentOperations; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	s.signal()
}
func (s *Service) Wait() { s.wg.Wait() }
func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		op, ok, e := s.repo.ClaimOperation(ctx)
		if e != nil {
			s.log.Error("claim operation", "error", e)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(2 * time.Second):
			}
			continue
		}
		s.process(ctx, op)
	}
}
func (s *Service) process(parent context.Context, o domain.Operation) {
	ctx, cancel := context.WithTimeout(parent, s.cfg.Server.OperationTimeout)
	defer cancel()
	start := s.clock.Now()
	s.metrics.Operations.WithLabelValues(o.Type).Inc()
	e := s.execute(ctx, o)
	if e != nil {
		s.metrics.OperationFailures.WithLabelValues(o.Type).Inc()
		_ = s.repo.FailOperation(parent, o.ID, "operation_failed", logging.Redact(e.Error()))
		var rolledBack *rollbackError
		if d, de := s.repo.GetDeployment(parent, o.DeploymentID); !errors.As(e, &rolledBack) && de == nil && d.State != domain.StateDeleted && domain.CanTransition(d.State, domain.StateFailed) {
			step, _, _ := strings.Cut(e.Error(), ":")
			_ = s.repo.UpdateState(parent, o.DeploymentID, domain.StateFailed, step)
		}
		s.log.Error("operation failed", "operation_id", o.ID, "deployment_id", o.DeploymentID, "type", o.Type, "error", e)
	} else {
		result, _ := json.Marshal(map[string]any{"deployment_id": o.DeploymentID, "status": "succeeded"})
		_ = s.repo.CompleteOperation(parent, o.ID, result)
		s.log.Info("operation completed", "operation_id", o.ID, "deployment_id", o.DeploymentID, "type", o.Type, "duration_ms", s.clock.Now().Sub(start).Milliseconds())
	}
	s.metrics.OperationDuration.WithLabelValues(o.Type).Observe(s.clock.Now().Sub(start).Seconds())
}
func (s *Service) execute(ctx context.Context, o domain.Operation) error {
	switch o.Type {
	case OpCreate:
		return s.provision(ctx, o)
	case OpStart:
		return s.start(ctx, o)
	case OpStop:
		return s.stop(ctx, o)
	case OpRestart:
		if e := s.stop(ctx, o); e != nil {
			return e
		}
		return s.start(ctx, o)
	case OpUpgrade:
		return s.upgrade(ctx, o)
	case OpDeleteSoft:
		return s.remove(ctx, o, false)
	case OpDeletePurge:
		return s.remove(ctx, o, true)
	case OpAdminReset:
		return s.resetAdmin(ctx, o)
	default:
		return fmt.Errorf("unknown operation type %q", o.Type)
	}
}

func (s *Service) resetAdmin(ctx context.Context, o domain.Operation) error {
	decrypted, err := s.secrets.Decrypt(o.Payload)
	if err != nil {
		return err
	}
	path, err := s.secrets.MaterializeNamed(o.DeploymentID, "panel_admin_reset.json", decrypted)
	if err != nil {
		return err
	}
	defer func() { _ = s.secrets.RemoveNamed(o.DeploymentID, "panel_admin_reset.json") }()
	_ = path // The container sees the stable /run/secrets path through its existing secret directory mounts.
	return s.docker.Exec(ctx, o.DeploymentID, s.cfg.Panel.AdminResetCommand)
}
func (s *Service) step(ctx context.Context, o domain.Operation, name string, fn func() error) error {
	_ = s.repo.RecordStep(ctx, o.ID, name, "running", "")
	if e := fn(); e != nil {
		_ = s.repo.RecordStep(ctx, o.ID, name, "failed", logging.Redact(e.Error()))
		return fmt.Errorf("%s: %w", name, e)
	}
	return s.repo.RecordStep(ctx, o.ID, name, "succeeded", "")
}
func (s *Service) transition(ctx context.Context, d *domain.Deployment, to domain.State) error {
	if d.State == to {
		return nil
	}
	if !domain.CanTransition(d.State, to) {
		return nil
	}
	if e := s.repo.UpdateState(ctx, d.Request.DeploymentID, to, ""); e != nil {
		return e
	}
	d.State = to
	return nil
}
func (s *Service) provision(ctx context.Context, o domain.Operation) error {
	d, e := s.repo.GetDeployment(ctx, o.DeploymentID)
	if e != nil {
		return e
	}
	secretBundle, e := s.decryptPermanentSecrets(d.EncryptedSecret)
	if e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StateCreatingDatabase); e != nil {
		return e
	}
	if e = s.step(ctx, o, "create_database", func() error {
		return s.postgres.EnsureRoleAndDatabase(ctx, d.Request.Database.DatabaseName, d.Request.Database.Username, secretBundle.DatabasePassword, d.Request.DeploymentID)
	}); e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StatePullingImage); e != nil {
		return e
	}
	if e = s.step(ctx, o, "ensure_networks", func() error {
		if e := s.docker.EnsureNetwork(ctx, s.cfg.Docker.FrontendNetwork, false); e != nil {
			return e
		}
		return s.docker.EnsureNetwork(ctx, s.cfg.Docker.EgressNetwork, true)
	}); e != nil {
		return e
	}
	if e = s.step(ctx, o, "pull_image", func() error { return s.docker.PullImage(ctx, d.Request.Image) }); e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StateCreatingContainer); e != nil {
		return e
	}
	secretFiles, e := s.materializeSecrets(d, secretBundle)
	if e != nil {
		return e
	}
	if e = s.step(ctx, o, "create_container", func() error { _, e := s.docker.CreateContainer(ctx, s.spec(d, secretFiles)); return e }); e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StateStarting); e != nil {
		return e
	}
	if e = s.step(ctx, o, "start_container", func() error { return s.docker.StartContainer(ctx, d.Request.DeploymentID) }); e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StateMigrating); e != nil {
		return e
	}
	if e = s.step(ctx, o, "migrate", func() error { return s.docker.Exec(ctx, d.Request.DeploymentID, s.cfg.Panel.MigrationCommand) }); e != nil {
		return e
	}
	d.EncryptedBootstrap = nil
	if e = s.repo.SaveDeployment(ctx, d); e != nil {
		return e
	}
	_ = s.secrets.RemoveNamed(d.Request.DeploymentID, "panel_bootstrap.json")
	if e = s.transition(ctx, &d, domain.StateHealthchecking); e != nil {
		return e
	}
	if e = s.step(ctx, o, "healthcheck", func() error {
		return s.health.Wait(ctx, d.Request.DeploymentID, d.Request.Healthcheck.Path, time.Duration(d.Request.Healthcheck.TimeoutSeconds)*time.Second)
	}); e != nil {
		return e
	}
	return s.transition(ctx, &d, domain.StateActive)
}
func (s *Service) materializeSecrets(d domain.Deployment, bundle permanentSecrets) (map[string]string, error) {
	files := map[string]string{}
	values := map[string]string{"postgres_password": bundle.DatabasePassword, "app_key": bundle.AppKey, "internal_secret": bundle.InternalSecret}
	if len(d.EncryptedBootstrap) > 0 {
		bootstrap, err := s.secrets.Decrypt(d.EncryptedBootstrap)
		if err != nil {
			return nil, err
		}
		values["panel_bootstrap.json"] = bootstrap
	}
	for name, value := range values {
		if value == "" {
			continue
		}
		path, err := s.secrets.MaterializeNamed(d.Request.DeploymentID, name, value)
		if err != nil {
			return nil, err
		}
		files[name] = path
	}
	return files, nil
}

func (s *Service) spec(d domain.Deployment, secretFiles map[string]string) domain.ContainerSpec {
	env := map[string]string{}
	for k, v := range d.Request.Environment {
		env[k] = v
	}
	env["PGHOST"] = s.cfg.Postgres.Host
	env["PGPORT"] = strconv.Itoa(s.cfg.Postgres.Port)
	env["PGDATABASE"] = d.Request.Database.DatabaseName
	env["PGUSER"] = d.Request.Database.Username
	env["PGPASSWORD_FILE"] = "/run/secrets/postgres_password"
	env["DB_PASSWORD_FILE"] = "/run/secrets/postgres_password"
	env["APP_KEY_FILE"] = "/run/secrets/app_key"
	env["PANEL_BOOTSTRAP_FILE"] = "/run/secrets/panel_bootstrap.json"
	env["CENTRALCLOUD_INTERNAL_SECRET_FILE"] = "/run/secrets/internal_secret"
	env["PANEL_MANAGED"] = "true"
	return domain.ContainerSpec{Deployment: d.Request, Environment: env, SecretFiles: secretFiles, StorageDirectory: filepath.Join(s.cfg.Storage.PanelDirectory, d.Request.DeploymentID), ManagementLabels: ManagementLabels(d.Request), TraefikLabels: TraefikLabels(d.Request, s.cfg), FrontendNetwork: s.cfg.Docker.FrontendNetwork, EgressNetwork: s.cfg.Docker.EgressNetwork, User: s.cfg.Docker.PanelUser, PidsLimit: s.cfg.Docker.PidsLimit}
}
func ManagementLabels(r contracts.CreateDeploymentRequest) map[string]string {
	version := r.Image
	if i := strings.LastIndexAny(version, ":@"); i >= 0 {
		version = version[i+1:]
	}
	return map[string]string{"centralcloud.managed": "true", "centralcloud.deployment_id": r.DeploymentID, "centralcloud.project_id": r.ProjectID, "centralcloud.hostname": r.Hostname, "centralcloud.version": version}
}
func TraefikLabels(r contracts.CreateDeploymentRequest, c config.Config) map[string]string {
	id := "cc" + strings.ReplaceAll(r.DeploymentID, "-", "")
	labels := map[string]string{"traefik.enable": "true", "traefik.docker.network": c.Docker.FrontendNetwork, "traefik.http.routers." + id + ".rule": "Host(`" + r.Hostname + "`)", "traefik.http.routers." + id + ".entrypoints": c.Traefik.Entrypoint, "traefik.http.services." + id + ".loadbalancer.server.port": "8080"}
	if c.Traefik.CertificateResolver != "" {
		labels["traefik.http.routers."+id+".tls"] = "true"
		labels["traefik.http.routers."+id+".tls.certresolver"] = c.Traefik.CertificateResolver
	}
	return labels
}
func (s *Service) start(ctx context.Context, o domain.Operation) error {
	d, e := s.repo.GetDeployment(ctx, o.DeploymentID)
	if e != nil {
		return e
	}
	if d.State != domain.StateStopped && d.State != domain.StateStarting {
		return fmt.Errorf("deployment must be stopped")
	}
	if d.State == domain.StateStopped {
		if e = s.transition(ctx, &d, domain.StateStarting); e != nil {
			return e
		}
	}
	if e = s.step(ctx, o, "start_container", func() error { return s.docker.StartContainer(ctx, d.Request.DeploymentID) }); e != nil {
		return e
	}
	if e = s.transition(ctx, &d, domain.StateHealthchecking); e != nil {
		return e
	}
	if e = s.health.Wait(ctx, d.Request.DeploymentID, d.Request.Healthcheck.Path, time.Duration(d.Request.Healthcheck.TimeoutSeconds)*time.Second); e != nil {
		return e
	}
	return s.transition(ctx, &d, domain.StateActive)
}
func (s *Service) stop(ctx context.Context, o domain.Operation) error {
	d, e := s.repo.GetDeployment(ctx, o.DeploymentID)
	if e != nil {
		return e
	}
	if d.State == domain.StateStopped {
		return nil
	}
	if d.State != domain.StateActive {
		return fmt.Errorf("deployment must be active")
	}
	if e = s.step(ctx, o, "stop_container", func() error { return s.docker.StopContainer(ctx, d.Request.DeploymentID, 30*time.Second) }); e != nil {
		return e
	}
	return s.transition(ctx, &d, domain.StateStopped)
}
func (s *Service) upgrade(ctx context.Context, o domain.Operation) error {
	var req contracts.UpgradeRequest
	if e := json.Unmarshal(o.Payload, &req); e != nil {
		return e
	}
	if req.Image != s.cfg.Docker.PanelImageRepository && !strings.HasPrefix(req.Image, s.cfg.Docker.PanelImageRepository+":") && !strings.HasPrefix(req.Image, s.cfg.Docker.PanelImageRepository+"@") {
		return fmt.Errorf("image repository is not allowed")
	}
	d, e := s.repo.GetDeployment(ctx, o.DeploymentID)
	if e != nil {
		return e
	}
	if d.State != domain.StateActive && d.State != domain.StateStopped {
		return fmt.Errorf("deployment cannot be upgraded from %s", d.State)
	}
	wasStopped := d.State == domain.StateStopped
	oldImage := d.Request.Image
	if req.Image == oldImage {
		return domain.ErrConflict
	}
	if e = s.transition(ctx, &d, domain.StateUpdating); e != nil {
		return e
	}
	secretBundle, e := s.decryptPermanentSecrets(d.EncryptedSecret)
	if e != nil {
		return e
	}
	backup, e := s.backups.Create(ctx, d, secretBundle.DatabasePassword)
	if e != nil {
		return e
	}
	if e = s.docker.PullImage(ctx, req.Image); e != nil {
		return e
	}
	_ = s.docker.StopContainer(ctx, d.Request.DeploymentID, 30*time.Second)
	if e = s.docker.RemoveContainer(ctx, d.Request.DeploymentID); e != nil {
		return e
	}
	d.Request.Image = req.Image
	if e = s.repo.SaveDeployment(ctx, d); e != nil {
		return e
	}
	secretFiles, e := s.materializeSecrets(d, secretBundle)
	if e == nil {
		_, e = s.docker.CreateContainer(ctx, s.spec(d, secretFiles))
	}
	if e == nil {
		e = s.docker.StartContainer(ctx, d.Request.DeploymentID)
	}
	if e == nil {
		e = s.docker.Exec(ctx, d.Request.DeploymentID, s.cfg.Panel.MigrationCommand)
	}
	if e == nil {
		e = s.health.Wait(ctx, d.Request.DeploymentID, d.Request.Healthcheck.Path, time.Duration(d.Request.Healthcheck.TimeoutSeconds)*time.Second)
	}
	if e != nil {
		failed := e
		_ = s.docker.RemoveContainer(ctx, d.Request.DeploymentID)
		if re := s.backups.Restore(ctx, d, secretBundle.DatabasePassword, backup); re != nil {
			return fmt.Errorf("upgrade failed: %w; restore failed: %w", failed, re)
		}
		d.Request.Image = oldImage
		_ = s.repo.SaveDeployment(ctx, d)
		_, re := s.docker.CreateContainer(ctx, s.spec(d, secretFiles))
		if re == nil {
			re = s.docker.StartContainer(ctx, d.Request.DeploymentID)
		}
		if re == nil {
			re = s.health.Wait(ctx, d.Request.DeploymentID, d.Request.Healthcheck.Path, time.Duration(d.Request.Healthcheck.TimeoutSeconds)*time.Second)
		}
		if re != nil {
			return fmt.Errorf("upgrade failed: %w; old image recovery failed: %w", failed, re)
		}
		_ = s.transition(ctx, &d, domain.StateActive)
		return &rollbackError{err: failed}
	}
	if wasStopped {
		_ = s.docker.StopContainer(ctx, d.Request.DeploymentID, 30*time.Second)
		e = s.transition(ctx, &d, domain.StateStopped)
	} else {
		e = s.transition(ctx, &d, domain.StateActive)
	}
	_ = s.backups.Prune(ctx, d.Request.DeploymentID, 2, 7*24*time.Hour)
	return e
}

type rollbackError struct{ err error }

func (e *rollbackError) Error() string { return "upgrade rolled back: " + e.err.Error() }
func (e *rollbackError) Unwrap() error { return e.err }
func (s *Service) remove(ctx context.Context, o domain.Operation, purge bool) error {
	d, e := s.repo.GetDeployment(ctx, o.DeploymentID)
	if e != nil {
		return e
	}
	if d.State != domain.StateDeleting {
		if !domain.CanTransition(d.State, domain.StateDeleting) {
			return fmt.Errorf("deployment cannot be deleted from %s", d.State)
		}
		if e = s.transition(ctx, &d, domain.StateDeleting); e != nil {
			return e
		}
	}
	_ = s.docker.StopContainer(ctx, d.Request.DeploymentID, 30*time.Second)
	if e = s.docker.RemoveContainer(ctx, d.Request.DeploymentID); e != nil {
		return e
	}
	_ = s.secrets.Remove(d.Request.DeploymentID)
	if purge {
		if e = s.postgres.DropRoleAndDatabase(ctx, d.Request.Database.DatabaseName, d.Request.Database.Username, d.Request.DeploymentID); e != nil {
			return e
		}
	}
	if e = s.transition(ctx, &d, domain.StateDeleted); e != nil {
		return e
	}
	if purge {
		return s.repo.DeleteDeploymentMaterial(ctx, d.Request.DeploymentID)
	}
	return nil
}

func (s *Service) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	return s.repo.GetDeployment(ctx, id)
}
func (s *Service) ListDeployments(ctx context.Context) ([]domain.Deployment, error) {
	return s.repo.ListDeployments(ctx)
}
func (s *Service) GetOperation(ctx context.Context, id string) (domain.Operation, error) {
	return s.repo.GetOperation(ctx, id)
}
func (s *Service) ReadLogs(ctx context.Context, id string, since time.Time, limit int) ([]string, time.Time, error) {
	lines, next, e := s.docker.Logs(ctx, id, since, limit)
	for i := range lines {
		lines[i] = logging.Redact(lines[i])
	}
	return lines, next, e
}
