package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/internal/logging"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type Client struct {
	cli          *client.Client
	registryAuth string
	traefikName  string
}

func New(socket, usernameFile, tokenFile, traefikName string) (*Client, error) {
	c, e := client.NewClientWithOpts(client.WithHost(socket), client.WithAPIVersionNegotiation())
	if e != nil {
		return nil, e
	}
	result := &Client{cli: c, traefikName: traefikName}
	if usernameFile != "" && tokenFile != "" {
		username, err := os.ReadFile(usernameFile) // #nosec G304 -- configured secret path.
		if err != nil {
			return nil, fmt.Errorf("read registry username: %w", err)
		}
		token, err := os.ReadFile(tokenFile) // #nosec G304 -- configured secret path.
		if err != nil {
			return nil, fmt.Errorf("read registry token: %w", err)
		}
		encoded, err := json.Marshal(registry.AuthConfig{Username: strings.TrimSpace(string(username)), Password: strings.TrimSpace(string(token))}) //nolint:gosec // Docker requires the registry credential in this auth payload.
		if err != nil {
			return nil, err
		}
		result.registryAuth = base64.URLEncoding.EncodeToString(encoded)
	}
	return result, nil
}
func (c *Client) Close() error                   { return c.cli.Close() }
func (c *Client) Ping(ctx context.Context) error { _, e := c.cli.Ping(ctx); return e }
func NetworkNames(id string) (domain.DeploymentNetworks, error) {
	if e := domain.ValidateDeploymentID(id); e != nil {
		return domain.DeploymentNetworks{}, e
	}
	suffix := strings.ReplaceAll(strings.ToLower(id), "-", "")
	return domain.DeploymentNetworks{Frontend: "centralcloud-fe-" + suffix, Backend: "centralcloud-be-" + suffix}, nil
}

func (c *Client) EnsureDeploymentNetworks(ctx context.Context, id string) (domain.DeploymentNetworks, error) {
	names, e := NetworkNames(id)
	if e != nil {
		return names, e
	}
	frontend, e := c.ensureNetwork(ctx, names.Frontend, false, id, "frontend")
	if e != nil {
		return names, e
	}
	backend, e := c.ensureNetwork(ctx, names.Backend, true, id, "backend")
	if e != nil {
		return names, e
	}
	if e = c.connectTraefik(ctx, frontend); e != nil {
		return names, e
	}
	for _, cfg := range backend.IPAM.Config {
		if cfg.Gateway != "" {
			names.BackendGateway = cfg.Gateway
			break
		}
	}
	if names.BackendGateway == "" {
		return names, fmt.Errorf("backend network %s has no gateway", names.Backend)
	}
	return names, nil
}

func (c *Client) ensureNetwork(ctx context.Context, name string, internal bool, id, role string) (network.Inspect, error) {
	n, e := c.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if e == nil {
		return n, validateNetwork(n, name, internal, id, role)
	}
	if !client.IsErrNotFound(e) {
		return network.Inspect{}, e
	}
	labels := map[string]string{"centralcloud.managed": "true", "centralcloud.deployment_id": strings.ToLower(id), "centralcloud.network_role": role}
	if _, e = c.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge", Internal: internal, Labels: labels}); e != nil {
		return network.Inspect{}, e
	}
	n, e = c.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if e != nil {
		return network.Inspect{}, e
	}
	return n, validateNetwork(n, name, internal, id, role)
}

func validateNetwork(n network.Inspect, name string, internal bool, id, role string) error {
	if n.Name != name || n.Driver != "bridge" || n.Internal != internal || n.Labels["centralcloud.managed"] != "true" || n.Labels["centralcloud.deployment_id"] != strings.ToLower(id) || n.Labels["centralcloud.network_role"] != role {
		return fmt.Errorf("refusing incompatible or unowned network %s", name)
	}
	return nil
}

func (c *Client) connectTraefik(ctx context.Context, frontend network.Inspect) error {
	if c.traefikName == "" {
		return errors.New("traefik container name is not configured")
	}
	traefik, e := c.cli.ContainerInspect(ctx, c.traefikName)
	if e != nil {
		return fmt.Errorf("inspect Traefik container %s: %w", c.traefikName, e)
	}
	if _, ok := frontend.Containers[traefik.ID]; ok {
		return nil
	}
	if e = c.cli.NetworkConnect(ctx, frontend.ID, traefik.ID, nil); e != nil {
		return fmt.Errorf("connect Traefik to network %s: %w", frontend.Name, e)
	}
	return nil
}

func (c *Client) RemoveDeploymentNetworks(ctx context.Context, id string) error {
	names, e := NetworkNames(id)
	if e != nil {
		return e
	}
	frontend, frontendExists, e := c.inspectOwnedNetwork(ctx, names.Frontend, false, id, "frontend")
	if e != nil {
		return e
	}
	backend, backendExists, e := c.inspectOwnedNetwork(ctx, names.Backend, true, id, "backend")
	if e != nil {
		return e
	}
	if frontendExists {
		traefik, inspectErr := c.cli.ContainerInspect(ctx, c.traefikName)
		if inspectErr != nil && !client.IsErrNotFound(inspectErr) {
			return inspectErr
		}
		if inspectErr == nil {
			if _, ok := frontend.Containers[traefik.ID]; ok {
				if e = c.cli.NetworkDisconnect(ctx, frontend.ID, traefik.ID, false); e != nil {
					return fmt.Errorf("disconnect Traefik from network %s: %w", frontend.Name, e)
				}
			}
		}
		if e = c.cli.NetworkRemove(ctx, frontend.ID); e != nil && !client.IsErrNotFound(e) {
			return e
		}
	}
	if backendExists {
		if e = c.cli.NetworkRemove(ctx, backend.ID); e != nil && !client.IsErrNotFound(e) {
			return e
		}
	}
	return nil
}

func (c *Client) inspectOwnedNetwork(ctx context.Context, name string, internal bool, id, role string) (network.Inspect, bool, error) {
	n, e := c.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if client.IsErrNotFound(e) {
		return network.Inspect{}, false, nil
	}
	if e != nil {
		return network.Inspect{}, false, e
	}
	if e = validateNetwork(n, name, internal, id, role); e != nil {
		return network.Inspect{}, false, e
	}
	return n, true, nil
}
func (c *Client) PullImage(ctx context.Context, ref string) error {
	if _, _, e := c.cli.ImageInspectWithRaw(ctx, ref); e == nil {
		return nil
	} else if !client.IsErrNotFound(e) {
		return fmt.Errorf("inspect image %s: %w", ref, e)
	}

	r, e := c.cli.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: c.registryAuth})
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	_, e = io.Copy(io.Discard, r)
	return e
}

func (c *Client) CreateContainer(ctx context.Context, s domain.ContainerSpec) (string, error) {
	name := "centralpanel-" + s.Deployment.DeploymentID
	if old, e := c.find(ctx, s.Deployment.DeploymentID); e == nil {
		return old.ID, nil
	} else if !errors.Is(e, domain.ErrNotFound) {
		return "", e
	}
	env := make([]string, 0, len(s.Environment))
	for k, v := range s.Environment {
		env = append(env, k+"="+v)
	}
	labels := map[string]string{}
	for k, v := range s.ManagementLabels {
		labels[k] = v
	}
	for k, v := range s.TraefikLabels {
		labels[k] = v
	}
	binds := []string{s.StorageDirectory + ":/app/storage"}
	names := make([]string, 0, len(s.SecretFiles))
	for name := range s.SecretFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	secretDirectory := ""
	for _, name := range names {
		if filepath.Base(name) != name {
			return "", fmt.Errorf("invalid secret name %q", name)
		}
		directory := filepath.Dir(s.SecretFiles[name])
		if secretDirectory != "" && directory != secretDirectory {
			return "", fmt.Errorf("secret files must share a directory")
		}
		secretDirectory = directory
	}
	if secretDirectory != "" {
		binds = append(binds, secretDirectory+":/run/secrets:ro")
	}
	resp, e := c.cli.ContainerCreate(ctx, &container.Config{Image: s.Deployment.Image, Env: env, Labels: labels, User: s.User}, containerHostConfig(s, binds), &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{s.FrontendNetwork: {}, s.BackendNetwork: {}}}, nil, name)
	return resp.ID, e
}

func containerHostConfig(s domain.ContainerSpec, binds []string) *container.HostConfig {
	pids := s.PidsLimit
	return &container.HostConfig{
		ReadonlyRootfs: true, SecurityOpt: []string{"no-new-privileges:true"}, CapDrop: []string{"ALL"},
		Resources: container.Resources{Memory: s.Deployment.Resources.MemoryBytes, NanoCPUs: int64(s.Deployment.Resources.CPULimit * 1e9), PidsLimit: &pids},
		Tmpfs:     map[string]string{"/tmp": "rw,noexec,nosuid,size=64m,mode=1777", "/run": "rw,noexec,nosuid,size=16m,mode=1777"},
		Binds:     binds, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
	}
}
func (c *Client) find(ctx context.Context, id string) (types.Container, error) {
	list, e := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "centralcloud.managed=true"), filters.Arg("label", "centralcloud.deployment_id="+id))})
	if e != nil {
		return types.Container{}, e
	}
	if len(list) == 0 {
		return types.Container{}, domain.ErrNotFound
	}
	if len(list) > 1 {
		return types.Container{}, fmt.Errorf("multiple managed containers for deployment")
	}
	return list[0], nil
}
func (c *Client) StartContainer(ctx context.Context, id string) error {
	if !strings.HasPrefix(id, "centralpanel-") {
		if found, e := c.find(ctx, id); e == nil {
			id = found.ID
		}
	}
	return c.cli.ContainerStart(ctx, id, container.StartOptions{})
}
func (c *Client) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	if found, e := c.find(ctx, id); e == nil {
		id = found.ID
	} else if errors.Is(e, domain.ErrNotFound) {
		return nil
	} else {
		return e
	}
	seconds := int(timeout.Seconds())
	return c.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &seconds})
}
func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	if found, e := c.find(ctx, id); e == nil {
		id = found.ID
	} else if errors.Is(e, domain.ErrNotFound) {
		return nil
	} else {
		return e
	}
	return c.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
}
func (c *Client) InspectDeployment(ctx context.Context, id string) (domain.ContainerInfo, error) {
	f, e := c.find(ctx, id)
	if e != nil {
		return domain.ContainerInfo{}, e
	}
	i, e := c.cli.ContainerInspect(ctx, f.ID)
	if e != nil {
		return domain.ContainerInfo{}, e
	}
	health := "none"
	if i.State != nil && i.State.Health != nil {
		health = i.State.Health.Status
	}
	names, _ := NetworkNames(id)
	address := networkAddress(i.NetworkSettings.Networks, names.Backend)
	return domain.ContainerInfo{ID: f.ID, Image: i.Config.Image, Status: i.State.Status, Health: health, Address: address}, nil
}

func networkAddress(networks map[string]*network.EndpointSettings, preferred string) string {
	if endpoint := networks[preferred]; endpoint != nil && endpoint.IPAddress != "" {
		return endpoint.IPAddress
	}
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if endpoint := networks[name]; endpoint != nil && endpoint.IPAddress != "" {
			return endpoint.IPAddress
		}
	}
	return ""
}
func (c *Client) Exec(ctx context.Context, id string, argv []string) error {
	f, e := c.find(ctx, id)
	if e != nil {
		return e
	}
	created, e := c.cli.ContainerExecCreate(ctx, f.ID, container.ExecOptions{Cmd: argv, AttachStdout: true, AttachStderr: true})
	if e != nil {
		return e
	}
	attached, e := c.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if e != nil {
		return e
	}
	defer attached.Close()
	output := &boundedExecOutput{limit: maxExecDiagnosticBytes}
	if _, e = stdcopy.StdCopy(output, output, attached.Reader); e != nil {
		return fmt.Errorf("read container command output: %w", e)
	}
	ins, e := c.cli.ContainerExecInspect(ctx, created.ID)
	if e != nil {
		return e
	}
	if ins.ExitCode != 0 {
		return execFailure(ins.ExitCode, output.diagnostic())
	}
	return nil
}

const maxExecDiagnosticBytes = 16 << 10

type boundedExecOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedExecOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.Buffer.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.Buffer.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return original, nil
}

func (w *boundedExecOutput) diagnostic() string {
	value := strings.ToValidUTF8(w.String(), "�")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return -1
	}, value)
	value = strings.TrimSpace(logging.Redact(value))
	if w.truncated {
		value += "\n[output truncated]"
	}
	return strings.TrimSpace(value)
}

func execFailure(exitCode int, diagnostic string) error {
	if diagnostic == "" {
		return fmt.Errorf("container command exited with code %d", exitCode)
	}
	return fmt.Errorf("container command exited with code %d\nDiagnostic de la commande :\n%s", exitCode, diagnostic)
}
func (c *Client) Logs(ctx context.Context, id string, cursor time.Time, limit int) ([]string, time.Time, error) {
	f, e := c.find(ctx, id)
	if e != nil {
		return nil, time.Time{}, e
	}
	until := ""
	if !cursor.IsZero() {
		until = cursor.Format(time.RFC3339Nano)
	}
	r, e := c.cli.ContainerLogs(ctx, f.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Until: until, Tail: strconv.Itoa(limit)})
	if e != nil {
		return nil, time.Time{}, e
	}
	defer func() { _ = r.Close() }()
	var decoded bytes.Buffer
	if _, e = stdcopy.StdCopy(&decoded, &decoded, r); e != nil {
		return nil, time.Time{}, e
	}
	var out []string
	oldest := cursor
	sc := bufio.NewScanner(&decoded)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			if t, e := time.Parse(time.RFC3339Nano, parts[0]); e == nil {
				if oldest.IsZero() || t.Before(oldest) {
					oldest = t
				}
				line = parts[1]
			}
		}
		out = append(out, line)
	}
	return out, oldest, sc.Err()
}
