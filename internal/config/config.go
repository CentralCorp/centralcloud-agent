package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Node struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	} `yaml:"node"`
	Server struct {
		Address          string        `yaml:"address"`
		ReadTimeout      time.Duration `yaml:"read_timeout"`
		WriteTimeout     time.Duration `yaml:"write_timeout"`
		IdleTimeout      time.Duration `yaml:"idle_timeout"`
		OperationTimeout time.Duration `yaml:"operation_timeout"`
		MaxRequestBytes  int64         `yaml:"max_request_bytes"`
		RatePerSecond    float64       `yaml:"rate_per_second"`
		RateBurst        int           `yaml:"rate_burst"`
	} `yaml:"server"`
	Security struct {
		Mode               string        `yaml:"mode"`
		CertificateFile    string        `yaml:"certificate_file"`
		PrivateKeyFile     string        `yaml:"private_key_file"`
		ClientCAFile       string        `yaml:"client_ca_file"`
		TokenFile          string        `yaml:"token_file"`
		MasterKeyFile      string        `yaml:"master_key_file"`
		AllowedClientSANs  []string      `yaml:"allowed_client_sans"`
		AllowedSourceCIDRs []string      `yaml:"allowed_source_cidrs"`
		TimestampSkew      time.Duration `yaml:"timestamp_skew"`
	} `yaml:"security"`
	Docker struct {
		Socket               string `yaml:"socket"`
		FrontendNetwork      string `yaml:"frontend_network"`
		EgressNetwork        string `yaml:"egress_network"`
		PanelImageRepository string `yaml:"panel_image_repository"`
		RequireImageDigest   bool   `yaml:"require_image_digest"`
		PanelUser            string `yaml:"panel_user"`
		PidsLimit            int64  `yaml:"pids_limit"`
		RegistryUsernameFile string `yaml:"registry_username_file"`
		RegistryTokenFile    string `yaml:"registry_token_file"`
	} `yaml:"docker"`
	Postgres struct {
		Host                      string `yaml:"host"`
		Port                      int    `yaml:"port"`
		AdministratorDatabase     string `yaml:"administrator_database"`
		AdministratorUsername     string `yaml:"administrator_username"`
		AdministratorPasswordFile string `yaml:"administrator_password_file"`
		BackupImage               string `yaml:"backup_image"`
		PanelHost                 string `yaml:"panel_host"`
	} `yaml:"postgres"`
	Traefik struct {
		ContainerName       string `yaml:"container_name"`
		DomainSuffix        string `yaml:"domain_suffix"`
		Entrypoint          string `yaml:"entrypoint"`
		CertificateResolver string `yaml:"certificate_resolver"`
	} `yaml:"traefik"`
	Limits struct {
		MaximumDeployments          int     `yaml:"maximum_deployments"`
		DefaultMemoryBytes          int64   `yaml:"default_memory_bytes"`
		DefaultCPULimit             float64 `yaml:"default_cpu_limit"`
		MaximumConcurrentOperations int     `yaml:"maximum_concurrent_operations"`
	} `yaml:"limits"`
	Panel struct {
		InstallCommand         []string `yaml:"install_command"`
		MigrationCommand       []string `yaml:"migration_command"`
		AdminResetCommand      []string `yaml:"admin_reset_command"`
		AllowedEnvironmentKeys []string `yaml:"allowed_environment_keys"`
	} `yaml:"panel"`
	Storage struct {
		DatabaseFile     string `yaml:"database_file"`
		RuntimeDirectory string `yaml:"runtime_directory"`
		BackupDirectory  string `yaml:"backup_directory"`
		PanelDirectory   string `yaml:"panel_directory"`
	} `yaml:"storage"`
}

func Defaults() Config {
	var c Config
	c.Server.Address = "127.0.0.1:9443"
	c.Server.ReadTimeout = 30 * time.Second
	c.Server.WriteTimeout = 30 * time.Second
	c.Server.IdleTimeout = 60 * time.Second
	c.Server.OperationTimeout = 10 * time.Minute
	c.Server.MaxRequestBytes = 1 << 20
	c.Server.RatePerSecond = 10
	c.Server.RateBurst = 20
	c.Security.Mode = "mtls"
	c.Security.TimestampSkew = 5 * time.Minute
	c.Docker.Socket = "unix:///var/run/docker.sock"
	c.Docker.FrontendNetwork = "centralcloud_frontend"
	c.Docker.EgressNetwork = "centralcloud_egress"
	c.Docker.PanelImageRepository = "ghcr.io/centralcorp-cloud/centralpanel-cloud"
	c.Docker.PanelUser = "10001:10001"
	c.Docker.PidsLimit = 256
	c.Postgres.Port = 5432
	c.Postgres.AdministratorDatabase = "postgres"
	c.Postgres.BackupImage = "postgres:17-alpine"
	c.Traefik.ContainerName = "centralcloud-traefik"
	c.Traefik.Entrypoint = "websecure"
	c.Traefik.CertificateResolver = "letsencrypt"
	c.Limits.MaximumDeployments = 50
	c.Limits.DefaultMemoryBytes = 402653184
	c.Limits.DefaultCPULimit = .5
	c.Limits.MaximumConcurrentOperations = 4
	c.Panel.InstallCommand = []string{"php", "artisan", "auto:install", "--bootstrap-file=/run/secrets/panel_bootstrap.json", "--no-interaction"}
	c.Panel.MigrationCommand = []string{"php", "artisan", "migrate", "--force", "--no-interaction"}
	c.Panel.AdminResetCommand = []string{"php", "artisan", "panel:admin-reset", "--bootstrap-file=/run/secrets/panel_admin_reset.json", "--no-interaction"}
	c.Storage.DatabaseFile = "/var/lib/centralcloud-agent/state.db"
	c.Storage.RuntimeDirectory = "/run/centralcloud-agent"
	c.Storage.BackupDirectory = "/var/lib/centralcloud-agent/backups"
	c.Storage.PanelDirectory = "/var/lib/centralcloud-agent/panels"
	return c
}

func Load(path string) (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(path) // #nosec G304 -- path is the explicit CLI configuration path.
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	applyEnv(&c)
	return c, c.Validate()
}

func applyEnv(c *Config) {
	set := func(k string, p *string) {
		if v, ok := os.LookupEnv(k); ok {
			*p = v
		}
	}
	set("CENTRALCLOUD_SERVER_ADDRESS", &c.Server.Address)
	set("CENTRALCLOUD_NODE_ID", &c.Node.ID)
	set("CENTRALCLOUD_NODE_NAME", &c.Node.Name)
	set("CENTRALCLOUD_SECURITY_MODE", &c.Security.Mode)
	set("CENTRALCLOUD_SECURITY_CERTIFICATE_FILE", &c.Security.CertificateFile)
	set("CENTRALCLOUD_SECURITY_PRIVATE_KEY_FILE", &c.Security.PrivateKeyFile)
	set("CENTRALCLOUD_SECURITY_CLIENT_CA_FILE", &c.Security.ClientCAFile)
	set("CENTRALCLOUD_SECURITY_TOKEN_FILE", &c.Security.TokenFile)
	set("CENTRALCLOUD_SECURITY_MASTER_KEY_FILE", &c.Security.MasterKeyFile)
	set("CENTRALCLOUD_DOCKER_SOCKET", &c.Docker.Socket)
	set("CENTRALCLOUD_DOCKER_REGISTRY_USERNAME_FILE", &c.Docker.RegistryUsernameFile)
	set("CENTRALCLOUD_DOCKER_REGISTRY_TOKEN_FILE", &c.Docker.RegistryTokenFile)
	set("CENTRALCLOUD_POSTGRES_PASSWORD_FILE", &c.Postgres.AdministratorPasswordFile)
	set("CENTRALCLOUD_POSTGRES_PANEL_HOST", &c.Postgres.PanelHost)
	set("CENTRALCLOUD_STORAGE_DATABASE_FILE", &c.Storage.DatabaseFile)
	set("CENTRALCLOUD_STORAGE_PANEL_DIRECTORY", &c.Storage.PanelDirectory)
	if v := os.Getenv("CENTRALCLOUD_LIMITS_MAXIMUM_DEPLOYMENTS"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			c.Limits.MaximumDeployments = n
		}
	}
}

func (c Config) Validate() error {
	var e []error
	if c.Node.ID != "" && !uuidRE.MatchString(strings.ToLower(c.Node.ID)) {
		e = append(e, errors.New("node.id must be a UUID"))
	}
	if c.Server.Address == "" {
		e = append(e, errors.New("server.address is required"))
	}
	if c.Security.Mode != "mtls" && c.Security.Mode != "token" {
		e = append(e, errors.New("security.mode must be mtls or token"))
	}
	if c.Security.MasterKeyFile == "" {
		e = append(e, errors.New("security.master_key_file is required"))
	}
	if c.Security.Mode == "mtls" && (c.Security.CertificateFile == "" || c.Security.PrivateKeyFile == "" || c.Security.ClientCAFile == "" || len(c.Security.AllowedClientSANs) == 0) {
		e = append(e, errors.New("mTLS certificate, key, CA and SAN allowlist are required"))
	}
	if c.Security.Mode == "token" && c.Security.TokenFile == "" {
		e = append(e, errors.New("security.token_file is required in token mode"))
	}
	for _, raw := range c.Security.AllowedSourceCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			e = append(e, fmt.Errorf("invalid security.allowed_source_cidrs entry %q", raw))
		}
	}
	if !strings.HasPrefix(c.Docker.Socket, "unix://") {
		e = append(e, errors.New("docker.socket must use unix://"))
	}
	if (c.Docker.RegistryUsernameFile == "") != (c.Docker.RegistryTokenFile == "") {
		e = append(e, errors.New("docker registry username and token files must be configured together"))
	}
	if c.Storage.PanelDirectory == "" {
		e = append(e, errors.New("storage.panel_directory is required"))
	}
	if c.Postgres.Host == "" || c.Postgres.AdministratorUsername == "" || c.Postgres.AdministratorPasswordFile == "" {
		e = append(e, errors.New("postgres host, administrator username and password file are required"))
	}
	if c.Traefik.DomainSuffix == "" {
		e = append(e, errors.New("traefik.domain_suffix is required"))
	}
	if strings.TrimSpace(c.Traefik.ContainerName) == "" {
		e = append(e, errors.New("traefik.container_name is required"))
	}
	if len(c.Panel.InstallCommand) == 0 {
		e = append(e, errors.New("panel.install_command is required"))
	}
	if len(c.Panel.MigrationCommand) == 0 {
		e = append(e, errors.New("panel.migration_command is required"))
	}
	if len(c.Panel.AdminResetCommand) == 0 {
		e = append(e, errors.New("panel.admin_reset_command is required"))
	}
	if c.Limits.MaximumDeployments < 1 || c.Limits.MaximumConcurrentOperations < 1 {
		e = append(e, errors.New("limits must be positive"))
	}
	seenEnvironmentKeys := make(map[string]struct{}, len(c.Panel.AllowedEnvironmentKeys))
	for _, key := range c.Panel.AllowedEnvironmentKeys {
		if !environmentKeyRE.MatchString(key) {
			e = append(e, fmt.Errorf("invalid panel.allowed_environment_keys entry %q", key))
		}
		if _, exists := seenEnvironmentKeys[key]; exists {
			e = append(e, fmt.Errorf("duplicate panel.allowed_environment_keys entry %q", key))
		}
		seenEnvironmentKeys[key] = struct{}{}
	}
	return errors.Join(e...)
}

var (
	uuidRE           = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	environmentKeyRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)
