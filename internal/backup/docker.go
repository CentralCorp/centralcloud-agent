package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

type Manager struct {
	cfg     config.Config
	cli     *client.Client
	secrets domain.SecretStore
	storage domain.DeploymentStorage
}

func New(c config.Config, secrets domain.SecretStore, storage domain.DeploymentStorage) (*Manager, error) {
	cli, e := client.NewClientWithOpts(client.WithHost(c.Docker.Socket), client.WithAPIVersionNegotiation())
	if e != nil {
		return nil, e
	}
	return &Manager{cfg: c, cli: cli, secrets: secrets, storage: storage}, nil
}
func (m *Manager) Close() error { return m.cli.Close() }
func (m *Manager) Create(ctx context.Context, d domain.Deployment, password string, networks domain.DeploymentNetworks) (string, error) {
	pull, e := m.cli.ImagePull(ctx, m.cfg.Postgres.BackupImage, image.PullOptions{})
	if e != nil {
		return "", e
	}
	_, e = io.Copy(io.Discard, pull)
	_ = pull.Close()
	if e != nil {
		return "", e
	}
	dir, e := m.storage.EnsureBackup(d.Request.DeploymentID)
	if e != nil {
		return "", e
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + ".dump"
	host := filepath.Join(dir, name)
	secret, e := m.secrets.Materialize(d.Request.DeploymentID, password)
	if e != nil {
		return "", e
	}
	postgresHost, e := m.containerPostgresHost(networks)
	if e != nil {
		return "", e
	}
	args := []string{"pg_dump", "--format=custom", "--no-owner", "--file=/backup/" + name, "--host=" + postgresHost, "--port=" + strconv.Itoa(m.cfg.Postgres.Port), "--username=" + d.Request.Database.Username, d.Request.Database.DatabaseName}
	if e = m.run(ctx, d.Request.DeploymentID, args, dir, secret, networks); e != nil {
		return "", e
	}
	raw, e := os.ReadFile(host) // #nosec G304 -- host is generated below the configured backup directory.
	if e != nil {
		return "", e
	}
	enc, e := m.secrets.Encrypt(string(raw))
	if e != nil {
		return "", e
	}
	encrypted := host + ".enc"
	if e = os.WriteFile(encrypted, enc, 0600); e != nil { // #nosec G703 -- encrypted is derived from a generated timestamp filename.
		return "", e
	}
	_ = os.Remove(host)
	return encrypted, nil
}
func (m *Manager) Restore(ctx context.Context, d domain.Deployment, password, encrypted string, networks domain.DeploymentNetworks) error {
	enc, e := os.ReadFile(encrypted) // #nosec G304 -- encrypted is a path returned by Create and kept in the state repository.
	if e != nil {
		return e
	}
	raw, e := m.secrets.Decrypt(enc)
	if e != nil {
		return e
	}
	dir := filepath.Dir(encrypted)
	name := filepath.Base(encrypted) + ".restore"
	host := filepath.Join(dir, name)
	if e = os.WriteFile(host, []byte(raw), 0600); e != nil { // #nosec G703 -- host is constrained to the deployment backup directory.
		return e
	}
	defer func() { _ = os.Remove(host) }()
	secret, e := m.secrets.Materialize(d.Request.DeploymentID, password)
	if e != nil {
		return e
	}
	postgresHost, e := m.containerPostgresHost(networks)
	if e != nil {
		return e
	}
	args := []string{"pg_restore", "--clean", "--if-exists", "--no-owner", "--host=" + postgresHost, "--port=" + strconv.Itoa(m.cfg.Postgres.Port), "--username=" + d.Request.Database.Username, "--dbname=" + d.Request.Database.DatabaseName, "/backup/" + name}
	return m.run(ctx, d.Request.DeploymentID, args, dir, secret, networks)
}
func (m *Manager) run(ctx context.Context, id string, args []string, backupDir, secret string, networks domain.DeploymentNetworks) error {
	// libpq requires the pgpass colon-separated format; generate it in the protected runtime directory without invoking a shell.
	pass, e := os.ReadFile(secret) // #nosec G304 -- secret is returned by the protected SecretStore.
	if e != nil {
		return e
	}
	pgpass := filepath.Join(filepath.Dir(secret), "pgpass")
	postgresHost, e := m.containerPostgresHost(networks)
	if e != nil {
		return e
	}
	line := fmt.Sprintf("%s:%d:*:%s:%s\n", postgresHost, m.cfg.Postgres.Port, extractUser(args), string(pass))
	if e = os.WriteFile(pgpass, []byte(line), 0400); e != nil { // #nosec G703 -- pgpass is constrained to the protected runtime directory.
		return e
	}
	defer func() { _ = os.Remove(pgpass) }()
	resp, e := m.cli.ContainerCreate(ctx, backupContainerConfig(m.cfg, id, args), &container.HostConfig{ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Binds: []string{backupDir + ":/backup", pgpass + ":/run/secrets/pgpass:ro"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=32m"}}, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{networks.Backend: {}}}, nil, "")
	if e != nil {
		return e
	}
	defer func() { _ = m.cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true}) }()
	if e = m.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); e != nil {
		return e
	}
	status, errs := m.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case e = <-errs:
		if e != nil {
			return e
		}
	case st := <-status:
		if st.StatusCode != 0 {
			return fmt.Errorf("PostgreSQL backup utility exited with code %d", st.StatusCode)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func backupContainerConfig(cfg config.Config, id string, args []string) *container.Config {
	return &container.Config{
		Image: cfg.Postgres.BackupImage,
		User:  cfg.Docker.PanelUser,
		Cmd:   args,
		Env:   []string{"PGPASSFILE=/run/secrets/pgpass"},
		Labels: map[string]string{
			"centralcloud.managed":       "true",
			"centralcloud.backup":        "true",
			"centralcloud.deployment_id": id,
		},
	}
}
func extractUser(args []string) string {
	for _, a := range args {
		const p = "--username="
		if len(a) > len(p) && a[:len(p)] == p {
			return a[len(p):]
		}
	}
	return ""
}

func (m *Manager) containerPostgresHost(networks domain.DeploymentNetworks) (string, error) {
	host := m.cfg.Postgres.PanelHost
	if host == "" {
		host = networks.BackendGateway
	}
	if host == "" {
		return "", errors.New("deployment backend network has no PostgreSQL gateway")
	}
	return host, nil
}
func (m *Manager) Prune(ctx context.Context, id string, keep int, maxAge time.Duration) error {
	dir, e := m.storage.EnsureBackup(id)
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if errors.Is(e, fs.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	backups := entries[:0]
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".enc" {
			backups = append(backups, entry)
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Name() > backups[j].Name() })
	now := time.Now()
	for i, en := range backups {
		info, _ := en.Info()
		if !en.IsDir() && (i >= keep || now.Sub(info.ModTime()) > maxAge) {
			if e = os.Remove(filepath.Join(dir, en.Name())); e != nil {
				return e
			}
		}
	}
	return nil
}

func (m *Manager) Purge(_ context.Context, id string) error {
	return m.storage.PurgeBackups(id)
}
