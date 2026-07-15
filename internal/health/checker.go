package health

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
)

type Checker struct {
	docker domain.DockerClient
	client *http.Client
}

func New(d domain.DockerClient) *Checker {
	return &Checker{docker: d, client: &http.Client{Timeout: 3 * time.Second}}
}
func (c *Checker) Wait(ctx context.Context, id, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		info, e := c.docker.InspectDeployment(ctx, id)
		if e == nil && info.Health == "healthy" && info.Address != "" {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+info.Address+":8080"+path, nil)
			if resp, e := c.client.Do(req); e == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("healthcheck timed out: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
