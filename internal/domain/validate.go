package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

var (
	uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dbRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	envRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	hostRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

var reservedEnv = map[string]bool{
	"PGHOST": true, "PGPORT": true, "PGDATABASE": true, "PGUSER": true, "PGPASSWORD": true, "PGPASSWORD_FILE": true, "DATABASE_URL": true,
}

func ValidateCreate(r *contracts.CreateDeploymentRequest, c config.Config) error {
	r.DeploymentID = strings.ToLower(r.DeploymentID)
	r.ProjectID = strings.ToLower(r.ProjectID)
	r.Hostname = strings.ToLower(strings.TrimSuffix(r.Hostname, "."))
	var errs []error
	if !uuidRE.MatchString(r.DeploymentID) {
		errs = append(errs, errors.New("deployment_id must be a UUID"))
	}
	if !uuidRE.MatchString(r.ProjectID) {
		errs = append(errs, errors.New("project_id must be a UUID"))
	}
	if !hostRE.MatchString(r.Hostname) || (r.Hostname != c.Traefik.DomainSuffix && !strings.HasSuffix(r.Hostname, "."+c.Traefik.DomainSuffix)) {
		errs = append(errs, errors.New("hostname is outside the configured domain suffix"))
	}
	if r.Image != c.Docker.PanelImageRepository && !strings.HasPrefix(r.Image, c.Docker.PanelImageRepository+":") && !strings.HasPrefix(r.Image, c.Docker.PanelImageRepository+"@") {
		errs = append(errs, errors.New("image repository is not allowed"))
	}
	if !dbRE.MatchString(r.Database.DatabaseName) {
		errs = append(errs, errors.New("invalid database_name"))
	}
	if !dbRE.MatchString(r.Database.Username) {
		errs = append(errs, errors.New("invalid database username"))
	}
	if r.Database.DatabaseName == r.Database.Username {
		errs = append(errs, errors.New("database_name and username must differ"))
	}
	if r.Resources.MemoryBytes == 0 {
		r.Resources.MemoryBytes = c.Limits.DefaultMemoryBytes
	}
	if r.Resources.CPULimit == 0 {
		r.Resources.CPULimit = c.Limits.DefaultCPULimit
	}
	if r.Resources.MemoryBytes < 64<<20 || r.Resources.CPULimit <= 0 {
		errs = append(errs, errors.New("invalid resource limits"))
	}
	if !strings.HasPrefix(r.Healthcheck.Path, "/") || strings.ContainsAny(r.Healthcheck.Path, "\r\n") {
		errs = append(errs, errors.New("healthcheck.path must be an absolute path"))
	}
	if r.Healthcheck.TimeoutSeconds == 0 {
		r.Healthcheck.TimeoutSeconds = 60
	}
	if r.Healthcheck.TimeoutSeconds < 1 || r.Healthcheck.TimeoutSeconds > 600 {
		errs = append(errs, errors.New("healthcheck timeout must be between 1 and 600 seconds"))
	}
	if len(r.Environment) > 128 {
		errs = append(errs, errors.New("too many environment variables"))
	}
	if strings.TrimSpace(r.Bootstrap.AdminName) == "" || len(r.Bootstrap.AdminName) > 255 {
		errs = append(errs, errors.New("bootstrap.admin_name is required"))
	}
	if !strings.Contains(r.Bootstrap.AdminEmail, "@") || len(r.Bootstrap.AdminEmail) > 255 {
		errs = append(errs, errors.New("bootstrap.admin_email is invalid"))
	}
	if len(r.Bootstrap.AdminPassword) < 12 || len(r.Bootstrap.AdminPassword) > 4096 {
		errs = append(errs, errors.New("bootstrap.admin_password must contain between 12 and 4096 characters"))
	}
	if len(r.Bootstrap.InternalSecret) < 32 || len(r.Bootstrap.InternalSecret) > 4096 {
		errs = append(errs, errors.New("bootstrap.internal_secret must contain between 32 and 4096 characters"))
	}
	for k, v := range r.Environment {
		if !envRE.MatchString(k) || reservedEnv[k] || len(v) > 4096 {
			errs = append(errs, fmt.Errorf("invalid or reserved environment variable %q", k))
		}
	}
	return errors.Join(errs...)
}

func ValidateDatabaseIdentifier(v string) error {
	if !dbRE.MatchString(v) {
		return fmt.Errorf("invalid PostgreSQL identifier %q", v)
	}
	return nil
}
