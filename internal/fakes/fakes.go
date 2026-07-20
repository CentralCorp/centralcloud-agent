package fakes

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

type Clock struct{ Time time.Time }

func (c *Clock) Now() time.Time { return c.Time }

type IDGenerator struct {
	Value string
	N     int
}

func (g *IDGenerator) New() string { g.N++; return g.Value }

type DockerClient struct {
	mu              sync.Mutex
	Spec            domain.ContainerSpec
	Exists, Running bool
	Calls           []string
	ExecCommands    [][]string
	LogsValue       []string
}

func (d *DockerClient) add(v string)               { d.mu.Lock(); defer d.mu.Unlock(); d.Calls = append(d.Calls, v) }
func (d *DockerClient) Ping(context.Context) error { return nil }
func (d *DockerClient) EnsureDeploymentNetworks(_ context.Context, id string) (domain.DeploymentNetworks, error) {
	suffix := strings.ReplaceAll(id, "-", "")
	n := domain.DeploymentNetworks{Frontend: "centralcloud-fe-" + suffix, Backend: "centralcloud-be-" + suffix, BackendGateway: "172.30.0.1"}
	d.add("networks:ensure")
	return n, nil
}
func (d *DockerClient) RemoveDeploymentNetworks(context.Context, string) error {
	d.add("networks:remove")
	return nil
}
func (d *DockerClient) PullImage(_ context.Context, i string) error { d.add("pull:" + i); return nil }
func (d *DockerClient) CreateContainer(_ context.Context, s domain.ContainerSpec) (string, error) {
	d.add("create")
	d.Spec = s
	d.Exists = true
	return "fake-container", nil
}
func (d *DockerClient) StartContainer(context.Context, string) error {
	d.add("start")
	d.Running = true
	return nil
}
func (d *DockerClient) StopContainer(context.Context, string, time.Duration) error {
	d.add("stop")
	d.Running = false
	return nil
}
func (d *DockerClient) RemoveContainer(context.Context, string) error {
	d.add("remove")
	d.Exists = false
	return nil
}
func (d *DockerClient) InspectDeployment(context.Context, string) (domain.ContainerInfo, error) {
	if !d.Exists {
		return domain.ContainerInfo{}, domain.ErrNotFound
	}
	status := "exited"
	health := "none"
	if d.Running {
		status = "running"
		health = "healthy"
	}
	return domain.ContainerInfo{ID: "fake-container", Status: status, Health: health, Address: "127.0.0.1"}, nil
}
func (d *DockerClient) Exec(_ context.Context, _ string, command []string) error {
	d.add("exec")
	d.mu.Lock()
	d.ExecCommands = append(d.ExecCommands, append([]string(nil), command...))
	d.mu.Unlock()
	return nil
}
func (d *DockerClient) Logs(context.Context, string, time.Time, int) ([]string, time.Time, error) {
	return d.LogsValue, time.Now(), nil
}

type PostgresProvisioner struct {
	mu                           sync.Mutex
	Created, Dropped             bool
	Database, Username, Password string
}

func (p *PostgresProvisioner) Ping(context.Context) error { return nil }
func (p *PostgresProvisioner) EnsureRoleAndDatabase(_ context.Context, db, user, password, marker string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Created = true
	p.Database = db
	p.Username = user
	p.Password = password
	return nil
}
func (p *PostgresProvisioner) DropRoleAndDatabase(context.Context, string, string, string) error {
	p.Dropped = true
	return nil
}

type HealthChecker struct {
	Err error
	N   int
}

func (h *HealthChecker) Wait(context.Context, string, string, time.Duration) error {
	h.N++
	return h.Err
}

type SequenceHealthChecker struct {
	Errors []error
	N      int
}

func (h *SequenceHealthChecker) Wait(context.Context, string, string, time.Duration) error {
	if h.N >= len(h.Errors) {
		return nil
	}
	err := h.Errors[h.N]
	h.N++
	return err
}

type BackupManager struct{ Created, Restored, Pruned bool }

func (b *BackupManager) Create(context.Context, domain.Deployment, string, domain.DeploymentNetworks) (string, error) {
	b.Created = true
	return "backup", nil
}
func (b *BackupManager) Restore(context.Context, domain.Deployment, string, string, domain.DeploymentNetworks) error {
	b.Restored = true
	return nil
}
func (b *BackupManager) Purge(context.Context, string) error { return nil }

type DeploymentStorage struct {
	Root                       string
	PanelPurged, BackupsPurged bool
}

func (s *DeploymentStorage) EnsurePanel(id string) (string, error) {
	return filepath.Join(s.Root, "panels", id), nil
}
func (s *DeploymentStorage) PurgePanel(string) error { s.PanelPurged = true; return nil }
func (s *DeploymentStorage) EnsureBackup(id string) (string, error) {
	return filepath.Join(s.Root, "backups", id), nil
}
func (s *DeploymentStorage) PurgeBackups(string) error { s.BackupsPurged = true; return nil }
func (b *BackupManager) Prune(context.Context, string, int, time.Duration) error {
	b.Pruned = true
	return nil
}

type ResourceCollector struct {
	Value contracts.ResourceResponse
	Err   error
}

func (r *ResourceCollector) Collect(context.Context) (contracts.ResourceResponse, error) {
	return r.Value, r.Err
}

// StateRepository is intentionally small and deterministic for service tests; SQLite tests cover transactional behavior.
type StateRepository struct {
	mu          sync.Mutex
	Deployments map[string]domain.Deployment
	Operations  map[string]domain.Operation
	Queue       []string
	Idempotency map[string][]byte
	Hashes      map[string]string
}

func NewStateRepository() *StateRepository {
	return &StateRepository{Deployments: map[string]domain.Deployment{}, Operations: map[string]domain.Operation{}, Idempotency: map[string][]byte{}, Hashes: map[string]string{}}
}
func (r *StateRepository) Ping(context.Context) error { return nil }
func (r *StateRepository) CreateDeployment(_ context.Context, d domain.Deployment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Deployments[d.Request.DeploymentID]; ok {
		return domain.ErrConflict
	}
	r.Deployments[d.Request.DeploymentID] = d
	return nil
}
func (r *StateRepository) SaveDeployment(_ context.Context, d domain.Deployment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Deployments[d.Request.DeploymentID] = d
	return nil
}
func (r *StateRepository) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Deployments[id]
	if !ok {
		return d, domain.ErrNotFound
	}
	return d, nil
}
func (r *StateRepository) ListDeployments(context.Context) ([]domain.Deployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Deployment, 0, len(r.Deployments))
	for _, d := range r.Deployments {
		out = append(out, d)
	}
	return out, nil
}
func (r *StateRepository) UpdateState(_ context.Context, id string, s domain.State, failed string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Deployments[id]
	if !ok {
		return domain.ErrNotFound
	}
	if d.State != s {
		if e := domain.ValidateTransition(d.State, s); e != nil {
			return e
		}
	}
	d.State = s
	d.FailedStep = failed
	r.Deployments[id] = d
	return nil
}
func (r *StateRepository) DeleteDeploymentMaterial(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.Deployments[id]
	d.EncryptedSecret = nil
	d.CredentialsRef = ""
	r.Deployments[id] = d
	return nil
}
func (r *StateRepository) CountDeployments(context.Context) (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := 0
	for _, d := range r.Deployments {
		if d.State == domain.StateActive {
			a++
		}
	}
	return len(r.Deployments), a, nil
}
func (r *StateRepository) CreateOperation(_ context.Context, o domain.Operation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o.Status = "queued"
	r.Operations[o.ID] = o
	r.Queue = append(r.Queue, o.ID)
	return nil
}
func (r *StateRepository) GetOperation(_ context.Context, id string) (domain.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.Operations[id]
	if !ok {
		return o, domain.ErrNotFound
	}
	return o, nil
}
func (r *StateRepository) ClaimOperation(context.Context) (domain.Operation, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Queue) == 0 {
		return domain.Operation{}, false, nil
	}
	id := r.Queue[0]
	r.Queue = r.Queue[1:]
	o := r.Operations[id]
	o.Status = "running"
	r.Operations[id] = o
	return o, true, nil
}
func (r *StateRepository) CompleteOperation(_ context.Context, id string, result []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.Operations[id]
	o.Status = "succeeded"
	o.Result = result
	r.Operations[id] = o
	return nil
}
func (r *StateRepository) CompletePurge(_ context.Context, operationID, deploymentID string, result []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Deployments, deploymentID)
	o := r.Operations[operationID]
	o.Status = "succeeded"
	o.Result = result
	r.Operations[operationID] = o
	return nil
}
func (r *StateRepository) FailOperation(_ context.Context, id, code, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.Operations[id]
	o.Status = "failed"
	o.ErrorCode = code
	o.ErrorMessage = message
	r.Operations[id] = o
	return nil
}
func (r *StateRepository) RecordStep(context.Context, string, string, string, string) error {
	return nil
}
func (r *StateRepository) GetIdempotency(_ context.Context, key string) ([]byte, string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.Idempotency[key]
	return b, r.Hashes[key], ok, nil
}
func (r *StateRepository) PutIdempotency(_ context.Context, key, hash string, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Idempotency[key]; ok {
		return domain.ErrConflict
	}
	r.Idempotency[key] = b
	r.Hashes[key] = hash
	return nil
}
func (r *StateRepository) CreatePurgeToken(context.Context, string, []byte, time.Time) error {
	return nil
}
func (r *StateRepository) ConsumePurgeToken(context.Context, string, []byte, time.Time) (bool, error) {
	return true, nil
}
func (r *StateRepository) ResolveNodeID(_ context.Context, configured, generated string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	return generated, nil
}

var _ = errors.New
