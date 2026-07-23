package contracts

import "time"

type Resources struct {
	MemoryBytes int64   `json:"memory_bytes"`
	CPULimit    float64 `json:"cpu_limit"`
}

type Database struct {
	DatabaseName string `json:"database_name"`
	Username     string `json:"username"`
}

type Healthcheck struct {
	Path           string `json:"path"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type Bootstrap struct {
	AdminName      string `json:"admin_name"`
	AdminEmail     string `json:"admin_email"`
	AdminPassword  string `json:"admin_password,omitempty"`
	InternalSecret string `json:"internal_secret,omitempty"`
}

type CreateDeploymentRequest struct {
	DeploymentID string            `json:"deployment_id"`
	ProjectID    string            `json:"project_id"`
	Hostname     string            `json:"hostname"`
	Aliases      []string          `json:"aliases,omitempty"`
	Image        string            `json:"image"`
	Environment  map[string]string `json:"environment"`
	Resources    Resources         `json:"resources"`
	Database     Database          `json:"database"`
	Healthcheck  Healthcheck       `json:"healthcheck"`
	Bootstrap    Bootstrap         `json:"bootstrap"`
}

type UpgradeRequest struct {
	Image string `json:"image"`
}

type AdminResetRequest struct {
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
}

type AcceptedOperation struct {
	OperationID  string `json:"operation_id"`
	DeploymentID string `json:"deployment_id,omitempty"`
	Status       string `json:"status"`
}

type AcceptedCreateOperation struct {
	AcceptedOperation
	Aliases []string `json:"aliases"`
}

type Operation struct {
	ID           string         `json:"id"`
	DeploymentID string         `json:"deployment_id,omitempty"`
	Type         string         `json:"type"`
	Status       string         `json:"status"`
	Error        *ErrorBody     `json:"error,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type Deployment struct {
	DeploymentID   string            `json:"deployment_id"`
	ProjectID      string            `json:"project_id"`
	Hostname       string            `json:"hostname"`
	Aliases        []string          `json:"aliases"`
	Image          string            `json:"image"`
	State          string            `json:"state"`
	Environment    map[string]string `json:"environment,omitempty"`
	Resources      Resources         `json:"resources"`
	Database       Database          `json:"database"`
	Healthcheck    Healthcheck       `json:"healthcheck"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	FailedStep     string            `json:"failed_step,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type HealthResponse struct {
	NodeID          string   `json:"node_id"`
	NodeName        string   `json:"node_name"`
	AgentVersion    string   `json:"agent_version"`
	ProtocolVersion string   `json:"protocol_version"`
	Commit          string   `json:"commit,omitempty"`
	BuildDate       string   `json:"build_date,omitempty"`
	Status          string   `json:"status"`
	Version         string   `json:"version"`
	Docker          string   `json:"docker"`
	Postgres        string   `json:"postgres"`
	Database        string   `json:"database"`
	Capabilities    []string `json:"capabilities"`
}

type ReadyResponse struct {
	NodeID          string `json:"node_id"`
	NodeName        string `json:"node_name"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion string `json:"protocol_version"`
	Commit          string `json:"commit,omitempty"`
	BuildDate       string `json:"build_date,omitempty"`
	Status          string `json:"status"`
}

type ResourceResponse struct {
	NodeID                string `json:"node_id"`
	CPUCount              int    `json:"cpu_count"`
	MemoryTotalBytes      int64  `json:"memory_total_bytes"`
	MemoryAvailableBytes  int64  `json:"memory_available_bytes"`
	DiskTotalBytes        int64  `json:"disk_total_bytes"`
	DiskAvailableBytes    int64  `json:"disk_available_bytes"`
	DeploymentCount       int    `json:"deployment_count"`
	ActiveDeploymentCount int    `json:"active_deployment_count"`
}

type ErrorBody struct {
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type LogPage struct {
	Lines      []string `json:"lines"`
	NextCursor string   `json:"next_cursor,omitempty"`
}
