package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type Client struct{ cli *client.Client }

func New(socket string) (*Client, error) {
	c, e := client.NewClientWithOpts(client.WithHost(socket), client.WithAPIVersionNegotiation())
	if e != nil {
		return nil, e
	}
	return &Client{cli: c}, nil
}
func (c *Client) Close() error                   { return c.cli.Close() }
func (c *Client) Ping(ctx context.Context) error { _, e := c.cli.Ping(ctx); return e }
func (c *Client) EnsureNetwork(ctx context.Context, name string, internal bool) error {
	n, e := c.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if e == nil {
		if n.Internal != internal {
			return fmt.Errorf("network %s exists with incompatible Internal setting", name)
		}
		return nil
	}
	if !client.IsErrNotFound(e) {
		return e
	}
	_, e = c.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge", Internal: internal, Labels: map[string]string{"centralcloud.managed": "true"}})
	return e
}
func (c *Client) PullImage(ctx context.Context, ref string) error {
	r, e := c.cli.ImagePull(ctx, ref, image.PullOptions{})
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
	pids := s.PidsLimit
	resp, e := c.cli.ContainerCreate(ctx, &container.Config{Image: s.Deployment.Image, Env: env, Labels: labels, User: s.User}, &container.HostConfig{
		ReadonlyRootfs: true, SecurityOpt: []string{"no-new-privileges:true"}, CapDrop: []string{"ALL"},
		Resources: container.Resources{Memory: s.Deployment.Resources.MemoryBytes, NanoCPUs: int64(s.Deployment.Resources.CPULimit * 1e9), PidsLimit: &pids},
		Tmpfs:     map[string]string{"/tmp": "rw,noexec,nosuid,size=64m", "/run": "rw,noexec,nosuid,size=16m"},
		Binds:     []string{s.SecretFile + ":" + s.Environment["PGPASSWORD_FILE"] + ":ro"}, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
	}, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{s.FrontendNetwork: {}, s.EgressNetwork: {}}}, nil, name)
	return resp.ID, e
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
	address := ""
	for _, n := range i.NetworkSettings.Networks {
		if n.IPAddress != "" {
			address = n.IPAddress
			break
		}
	}
	return domain.ContainerInfo{ID: f.ID, Image: i.Config.Image, Status: i.State.Status, Health: health, Address: address}, nil
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
	_, _ = io.Copy(io.Discard, attached.Reader)
	ins, e := c.cli.ContainerExecInspect(ctx, created.ID)
	if e != nil {
		return e
	}
	if ins.ExitCode != 0 {
		return fmt.Errorf("migration command exited with code %d", ins.ExitCode)
	}
	return nil
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
