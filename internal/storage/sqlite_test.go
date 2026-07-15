package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/centralcorp/centralcloud-node-agent/pkg/contracts"
)

type clock struct{ t time.Time }

func (c clock) Now() time.Time { return c.t }
func TestSQLiteStateAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "state.db"), clock{time.Unix(1, 0).UTC()})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	d := domain.Deployment{Request: contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000"}, State: domain.StatePending}
	if e = s.CreateDeployment(ctx, d); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateState(ctx, d.Request.DeploymentID, domain.StateCreatingDatabase, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateState(ctx, d.Request.DeploymentID, domain.StateActive, ""); e == nil {
		t.Fatal("invalid transition accepted")
	}
	if e = s.PutIdempotency(ctx, "key", "hash", []byte("response")); e != nil {
		t.Fatal(e)
	}
	b, h, ok, e := s.GetIdempotency(ctx, "key")
	if e != nil || !ok || h != "hash" || string(b) != "response" {
		t.Fatalf("%q %q %v %v", b, h, ok, e)
	}
	if e = s.CreateDeployment(ctx, d); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("want conflict, got %v", e)
	}
}
