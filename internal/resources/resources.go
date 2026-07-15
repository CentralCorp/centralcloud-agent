package resources

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

type Collector struct {
	path string
	repo domain.StateRepository
}

func New(path string, repo domain.StateRepository) *Collector {
	return &Collector{path: path, repo: repo}
}
func (c *Collector) Collect(ctx context.Context) (contracts.ResourceResponse, error) {
	total, avail, e := memory()
	if e != nil {
		return contracts.ResourceResponse{}, e
	}
	var st syscall.Statfs_t
	if e = syscall.Statfs(c.path, &st); e != nil {
		return contracts.ResourceResponse{}, e
	}
	count, active, e := c.repo.CountDeployments(ctx)
	if e != nil {
		return contracts.ResourceResponse{}, e
	}
	return contracts.ResourceResponse{CPUCount: runtime.NumCPU(), MemoryTotalBytes: total, MemoryAvailableBytes: avail, DiskTotalBytes: diskBytes(st.Blocks, st.Bsize), DiskAvailableBytes: diskBytes(st.Bavail, st.Bsize), DeploymentCount: count, ActiveDeploymentCount: active}, nil
}
func diskBytes(blocks uint64, size int64) int64 {
	if size <= 0 || blocks == 0 {
		return 0
	}
	if blocks > uint64(^uint64(0)>>1)/uint64(size) { // #nosec G115 -- size is proven positive before conversion.
		return int64(^uint64(0) >> 1) // #nosec G115 -- this is exactly MaxInt64.
	}
	return int64(blocks) * size // #nosec G115 -- the overflow bound above proves blocks fits in int64.
}
func memory() (int64, int64, error) {
	f, e := os.Open("/proc/meminfo")
	if e != nil {
		return 0, 0, e
	}
	defer func() { _ = f.Close() }()
	vals := map[string]int64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		p := strings.Fields(s.Text())
		if len(p) >= 2 {
			n, _ := strconv.ParseInt(p[1], 10, 64)
			vals[strings.TrimSuffix(p[0], ":")] = n * 1024
		}
	}
	if vals["MemTotal"] == 0 {
		return 0, 0, fmt.Errorf("MemTotal missing")
	}
	return vals["MemTotal"], vals["MemAvailable"], s.Err()
}
