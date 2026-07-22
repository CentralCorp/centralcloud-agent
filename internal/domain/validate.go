package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
	"github.com/distribution/reference"
)

var (
	uuidRE        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dbRE          = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	envRE         = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	hostRE        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	hostLabelRE   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	imageDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

var reservedEnv = map[string]bool{
	"PGHOST": true, "PGPORT": true, "PGDATABASE": true, "PGUSER": true, "PGPASSWORD": true, "PGPASSWORD_FILE": true, "DATABASE_URL": true,
	"DB_PASSWORD_FILE": true, "APP_KEY_FILE": true, "PANEL_BOOTSTRAP_FILE": true, "CENTRALCLOUD_INTERNAL_SECRET_FILE": true, "PANEL_MANAGED": true,
	"APP_ENV": true, "APP_URL": true, "CENTRALPANEL_MODE": true, "CLOUD_PROJECT_ID": true,
}

var sensitiveEnvSegments = map[string]bool{
	"PASSWORD": true, "PASSWD": true, "TOKEN": true, "SECRET": true, "CREDENTIAL": true, "CREDENTIALS": true, "KEY": true, "AUTHORIZATION": true,
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
	if len(r.Aliases) > 1 {
		errs = append(errs, errors.New("aliases must contain at most one hostname"))
	}
	aliases := make([]string, 0, len(r.Aliases))
	seenAliases := make(map[string]struct{}, len(r.Aliases))
	for _, value := range r.Aliases {
		alias := strings.ToLower(strings.TrimSuffix(value, "."))
		switch {
		case ValidateDNSHostname(alias) != nil:
			errs = append(errs, errors.New("alias must be a valid ASCII DNS hostname"))
		case alias == r.Hostname:
			errs = append(errs, errors.New("alias must differ from hostname"))
		default:
			if _, exists := seenAliases[alias]; exists {
				errs = append(errs, errors.New("aliases must not contain duplicates"))
			} else {
				seenAliases[alias] = struct{}{}
			}
		}
		aliases = append(aliases, alias)
	}
	r.Aliases = aliases
	if err := ValidatePanelImage(r.Image, c); err != nil {
		errs = append(errs, err)
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
	allowedEnvironment := make(map[string]bool, len(c.Panel.AllowedEnvironmentKeys))
	for _, key := range c.Panel.AllowedEnvironmentKeys {
		allowedEnvironment[key] = true
	}
	for k, v := range r.Environment {
		switch {
		case !envRE.MatchString(k):
			errs = append(errs, fmt.Errorf("invalid environment variable key %q", k))
		case reservedEnv[k]:
			errs = append(errs, fmt.Errorf("reserved environment variable %q", k))
		case isSensitiveEnvironmentKey(k):
			errs = append(errs, fmt.Errorf("secret-like environment variable %q must use a secret file", k))
		case !allowedEnvironment[k]:
			errs = append(errs, fmt.Errorf("environment variable %q is not allowed", k))
		case len(v) > 4096:
			errs = append(errs, fmt.Errorf("environment variable %q value is too long", k))
		}
	}
	return errors.Join(errs...)
}

// ValidateDNSHostname accepts normalized, lowercase ASCII DNS hostnames. It is
// also used immediately before rendering Traefik rules so only this restricted
// alphabet can reach a Host matcher.
func ValidateDNSHostname(value string) error {
	if value == "" || len(value) > 253 || value != strings.ToLower(value) {
		return errors.New("invalid DNS hostname")
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return errors.New("IP addresses are not DNS hostnames")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || !hostLabelRE.MatchString(label) {
			return errors.New("invalid DNS hostname label")
		}
	}
	return nil
}

func ValidatePanelImage(value string, c config.Config) error {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return errors.New("invalid panel image reference")
	}
	allowed, err := reference.ParseNormalizedNamed(c.Docker.PanelImageRepository)
	if err != nil {
		return errors.New("configured panel image repository is invalid")
	}
	if reference.FamiliarName(reference.TrimNamed(named)) != reference.FamiliarName(reference.TrimNamed(allowed)) {
		return errors.New("image repository is not allowed")
	}
	if c.Docker.RequireImageDigest {
		digested, ok := named.(reference.Digested)
		if !ok || !imageDigestRE.MatchString(digested.Digest().String()) {
			return errors.New("panel image must include a valid sha256 digest")
		}
	}
	return nil
}

func isSensitiveEnvironmentKey(key string) bool {
	if key == "DATABASE_URL" || key == "APP_KEY" || key == "DB_PASSWORD" {
		return true
	}
	for _, segment := range strings.Split(key, "_") {
		if sensitiveEnvSegments[segment] {
			return true
		}
	}
	return false
}

func ValidateDatabaseIdentifier(v string) error {
	if !dbRE.MatchString(v) {
		return fmt.Errorf("invalid PostgreSQL identifier %q", v)
	}
	return nil
}

func ValidateDeploymentID(v string) error {
	if !uuidRE.MatchString(strings.ToLower(v)) {
		return errors.New("deployment_id must be a UUID")
	}
	return nil
}
