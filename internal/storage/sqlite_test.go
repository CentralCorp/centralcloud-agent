package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
	d := domain.Deployment{Request: contracts.CreateDeploymentRequest{DeploymentID: "123e4567-e89b-42d3-a456-426614174000", Aliases: []string{"panel.example.com"}}, State: domain.StatePending}
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
	stored, e := s.GetDeployment(ctx, d.Request.DeploymentID)
	if e != nil || len(stored.Request.Aliases) != 1 || stored.Request.Aliases[0] != "panel.example.com" {
		t.Fatalf("aliases were not persisted: %#v err=%v", stored.Request.Aliases, e)
	}
}

func TestDeploymentAliasesMigrationAndRestartPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.ExecContext(ctx, `CREATE TABLE deployments(
deployment_id TEXT PRIMARY KEY, request_json BLOB NOT NULL, state TEXT NOT NULL,
credentials_ref TEXT NOT NULL DEFAULT '', encrypted_secret BLOB, failed_step TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO deployments(deployment_id,request_json,state,created_at,updated_at) VALUES(
'123e4567-e89b-42d3-a456-426614174000','{"deployment_id":"123e4567-e89b-42d3-a456-426614174000","hostname":"legacy.cloud.centralcorp.fr"}','active','1970-01-01T00:00:01Z','1970-01-01T00:00:01Z');`); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, clock{time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	legacyDeployment, err := s.GetDeployment(ctx, "123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if legacyDeployment.Request.Aliases == nil || len(legacyDeployment.Request.Aliases) != 0 {
		t.Fatalf("legacy aliases were not initialized to []: %#v", legacyDeployment.Request.Aliases)
	}
	legacyDeployment.Request.Aliases = []string{"legacy.example.com"}
	if err = s.SaveDeployment(ctx, legacyDeployment); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, clock{time.Unix(3, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stored, err := s.GetDeployment(ctx, legacyDeployment.Request.DeploymentID)
	if err != nil || len(stored.Request.Aliases) != 1 || stored.Request.Aliases[0] != "legacy.example.com" {
		t.Fatalf("aliases did not survive restart: %#v err=%v", stored.Request.Aliases, err)
	}
	listed, err := s.ListDeployments(ctx)
	if err != nil || len(listed) != 1 || len(listed[0].Request.Aliases) != 1 {
		t.Fatalf("aliases missing from list: %#v err=%v", listed, err)
	}
	var aliasesJSON []byte
	if err = s.db.QueryRowContext(ctx, `SELECT aliases_json FROM deployments WHERE deployment_id=?`, stored.Request.DeploymentID).Scan(&aliasesJSON); err != nil {
		t.Fatal(err)
	}
	var aliases []string
	if err = json.Unmarshal(aliasesJSON, &aliases); err != nil || len(aliases) != 1 {
		t.Fatalf("invalid aliases_json column: %s err=%v", aliasesJSON, err)
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
