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

func TestNodeIDPersistsAndRejectsConfiguredMismatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path, clock{time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	generated := "123e4567-e89b-42d3-a456-426614174000"
	got, err := s.ResolveNodeID(ctx, "", generated)
	if err != nil || got != generated {
		t.Fatalf("node id=%q err=%v", got, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, clock{time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err = s.ResolveNodeID(ctx, "", "123e4567-e89b-42d3-a456-426614174001")
	if err != nil || got != generated {
		t.Fatalf("persisted node id=%q err=%v", got, err)
	}
	if _, err = s.ResolveNodeID(ctx, "123e4567-e89b-42d3-a456-426614174099", "unused"); err == nil {
		t.Fatal("configured node identity mismatch accepted")
	}
}

func TestCompletePurgeAtomicallyRemovesDeploymentAndCompletesOperation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), clock{time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := "123e4567-e89b-42d3-a456-426614174000"
	opID := "123e4567-e89b-42d3-a456-426614174001"
	d := domain.Deployment{Request: contracts.CreateDeploymentRequest{DeploymentID: id}, State: domain.StatePending, EncryptedSecret: []byte("ciphertext")}
	if err = s.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateOperation(ctx, domain.Operation{ID: opID, DeploymentID: id, Type: "delete_purge", Payload: []byte("encrypted")}); err != nil {
		t.Fatal(err)
	}
	if err = s.CompletePurge(ctx, opID, id, []byte(`{"status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetDeployment(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deployment remains after purge: %v", err)
	}
	op, err := s.GetOperation(ctx, opID)
	if err != nil || op.Status != "succeeded" || len(op.Payload) != 0 {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
}
