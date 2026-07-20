package domain

import (
	"context"
	"io"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

type Deployment struct {
	Request            contracts.CreateDeploymentRequest
	State              State
	CredentialsRef     string
	EncryptedSecret    []byte
	EncryptedBootstrap []byte
	FailedStep         string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Operation struct {
	ID, DeploymentID, Type, Status, ErrorCode, ErrorMessage string
	Payload, Result                                         []byte
	CreatedAt, UpdatedAt                                    time.Time
}

type ContainerSpec struct {
	Deployment                            contracts.CreateDeploymentRequest
	Environment                           map[string]string
	SecretFiles                           map[string]string
	StorageDirectory                      string
	ManagementLabels                      map[string]string
	TraefikLabels                         map[string]string
	FrontendNetwork, BackendNetwork, User string
	PidsLimit                             int64
}

type DeploymentNetworks struct {
	Frontend       string
	Backend        string
	BackendGateway string
}

type ContainerInfo struct{ ID, Image, Status, Health, Address string }

type DockerClient interface {
	Ping(context.Context) error
	EnsureDeploymentNetworks(context.Context, string) (DeploymentNetworks, error)
	RemoveDeploymentNetworks(context.Context, string) error
	PullImage(context.Context, string) error
	CreateContainer(context.Context, ContainerSpec) (string, error)
	StartContainer(context.Context, string) error
	StopContainer(context.Context, string, time.Duration) error
	RemoveContainer(context.Context, string) error
	InspectDeployment(context.Context, string) (ContainerInfo, error)
	Exec(context.Context, string, []string) error
	Logs(context.Context, string, time.Time, int) ([]string, time.Time, error)
}

type PostgresProvisioner interface {
	Ping(context.Context) error
	EnsureRoleAndDatabase(context.Context, string, string, string, string) error
	DropRoleAndDatabase(context.Context, string, string, string) error
}

type StateRepository interface {
	Ping(context.Context) error
	CreateDeployment(context.Context, Deployment) error
	SaveDeployment(context.Context, Deployment) error
	GetDeployment(context.Context, string) (Deployment, error)
	ListDeployments(context.Context) ([]Deployment, error)
	UpdateState(context.Context, string, State, string) error
	DeleteDeploymentMaterial(context.Context, string) error
	CountDeployments(context.Context) (int, int, error)
	CreateOperation(context.Context, Operation) error
	GetOperation(context.Context, string) (Operation, error)
	ClaimOperation(context.Context) (Operation, bool, error)
	CompleteOperation(context.Context, string, []byte) error
	CompletePurge(context.Context, string, string, []byte) error
	FailOperation(context.Context, string, string, string) error
	RecordStep(context.Context, string, string, string, string) error
	GetIdempotency(context.Context, string) ([]byte, string, bool, error)
	PutIdempotency(context.Context, string, string, []byte) error
	CreatePurgeToken(context.Context, string, []byte, time.Time) error
	ConsumePurgeToken(context.Context, string, []byte, time.Time) (bool, error)
	ResolveNodeID(context.Context, string, string) (string, error)
}

type HealthChecker interface {
	Wait(context.Context, string, string, time.Duration) error
}
type Clock interface{ Now() time.Time }
type IDGenerator interface{ New() string }
type SecretStore interface {
	Generate() (string, error)
	Encrypt(string) ([]byte, error)
	Decrypt([]byte) (string, error)
	Materialize(string, string) (string, error)
	MaterializeNamed(string, string, string) (string, error)
	RemoveNamed(string, string) error
	Remove(string) error
}
type ResourceCollector interface {
	Collect(context.Context) (contracts.ResourceResponse, error)
}
type BackupManager interface {
	Create(context.Context, Deployment, string, DeploymentNetworks) (string, error)
	Restore(context.Context, Deployment, string, string, DeploymentNetworks) error
	Prune(context.Context, string, int, time.Duration) error
	Purge(context.Context, string) error
}

type DeploymentStorage interface {
	EnsurePanel(string) (string, error)
	PurgePanel(string) error
	EnsureBackup(string) (string, error)
	PurgeBackups(string) error
}
type LogReader interface {
	Read(context.Context, string, time.Time, int) ([]string, time.Time, error)
}
type ReadCloser = io.ReadCloser
