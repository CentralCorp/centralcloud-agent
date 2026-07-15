package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	_ "modernc.org/sqlite"
)

type SQLite struct {
	db    *sql.DB
	clock domain.Clock
}

func Open(path string, clock domain.Clock) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db, clock: clock}
	if err = s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *SQLite) Close() error                   { return s.db.Close() }
func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *SQLite) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS deployments(
 deployment_id TEXT PRIMARY KEY, request_json BLOB NOT NULL, state TEXT NOT NULL,
 credentials_ref TEXT NOT NULL DEFAULT '', encrypted_secret BLOB, failed_step TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS operations(
 operation_id TEXT PRIMARY KEY, deployment_id TEXT NOT NULL DEFAULT '', type TEXT NOT NULL, status TEXT NOT NULL,
 payload BLOB, result BLOB, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS operations_status_created ON operations(status,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS operations_one_active_per_deployment ON operations(deployment_id) WHERE deployment_id<>'' AND status IN ('queued','running');
CREATE TABLE IF NOT EXISTS operation_steps(id INTEGER PRIMARY KEY AUTOINCREMENT,operation_id TEXT NOT NULL,step TEXT NOT NULL,status TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS idempotency_keys(key TEXT PRIMARY KEY,request_hash TEXT NOT NULL,response BLOB NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS purge_tokens(deployment_id TEXT NOT NULL,token_hash BLOB NOT NULL,expires_at TEXT NOT NULL,consumed_at TEXT,PRIMARY KEY(deployment_id,token_hash));
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT,deployment_id TEXT NOT NULL,event TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL);`)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE operations SET status='queued' WHERE status='running'`)
	}
	return err
}

func (s *SQLite) CreateDeployment(ctx context.Context, d domain.Deployment) error {
	b, err := json.Marshal(d.Request)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO deployments(deployment_id,request_json,state,credentials_ref,encrypted_secret,failed_step,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, d.Request.DeploymentID, b, d.State, d.CredentialsRef, d.EncryptedSecret, d.FailedStep, d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil && isConstraint(err) {
		return domain.ErrConflict
	}
	return err
}
func (s *SQLite) SaveDeployment(ctx context.Context, d domain.Deployment) error {
	b, e := json.Marshal(d.Request)
	if e != nil {
		return e
	}
	r, e := s.db.ExecContext(ctx, `UPDATE deployments SET request_json=?,state=?,credentials_ref=?,encrypted_secret=?,failed_step=?,updated_at=? WHERE deployment_id=?`, b, d.State, d.CredentialsRef, d.EncryptedSecret, d.FailedStep, s.clock.Now().Format(time.RFC3339Nano), d.Request.DeploymentID)
	if e == nil {
		n, _ := r.RowsAffected()
		if n == 0 {
			return domain.ErrNotFound
		}
	}
	return e
}
func scanDeployment(row interface{ Scan(...any) error }) (domain.Deployment, error) {
	var d domain.Deployment
	var b []byte
	var state, created, updated string
	err := row.Scan(&b, &state, &d.CredentialsRef, &d.EncryptedSecret, &d.FailedStep, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return d, domain.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(b, &d.Request); err != nil {
		return d, err
	}
	d.State = domain.State(state)
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return d, nil
}
func (s *SQLite) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	return scanDeployment(s.db.QueryRowContext(ctx, `SELECT request_json,state,credentials_ref,encrypted_secret,failed_step,created_at,updated_at FROM deployments WHERE deployment_id=?`, id))
}
func (s *SQLite) ListDeployments(ctx context.Context) ([]domain.Deployment, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT request_json,state,credentials_ref,encrypted_secret,failed_step,created_at,updated_at FROM deployments ORDER BY created_at`)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Deployment
	for rows.Next() {
		d, e := scanDeployment(rows)
		if e != nil {
			return nil, e
		}
		d.EncryptedSecret = nil
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *SQLite) UpdateState(ctx context.Context, id string, to domain.State, failedStep string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	var from string
	if e = tx.QueryRowContext(ctx, `SELECT state FROM deployments WHERE deployment_id=?`, id).Scan(&from); errors.Is(e, sql.ErrNoRows) {
		return domain.ErrNotFound
	} else if e != nil {
		return e
	}
	if domain.State(from) != to {
		if e = domain.ValidateTransition(domain.State(from), to); e != nil {
			return e
		}
	}
	now := s.clock.Now().Format(time.RFC3339Nano)
	if _, e = tx.ExecContext(ctx, `UPDATE deployments SET state=?,failed_step=?,updated_at=? WHERE deployment_id=?`, to, failedStep, now, id); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO audit(deployment_id,event,detail,created_at) VALUES(?,?,?,?)`, id, "state_changed", string(to), now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *SQLite) DeleteDeploymentMaterial(ctx context.Context, id string) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM deployments WHERE deployment_id=?`, id)
	return e
}
func (s *SQLite) CountDeployments(ctx context.Context) (int, int, error) {
	var n, a int
	e := s.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN state='active' THEN 1 ELSE 0 END),0) FROM deployments WHERE credentials_ref<>''`).Scan(&n, &a)
	return n, a, e
}

func (s *SQLite) CreateOperation(ctx context.Context, o domain.Operation) error {
	now := s.clock.Now()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO operations(operation_id,deployment_id,type,status,payload,created_at,updated_at)VALUES(?,?,?,?,?,?,?)`, o.ID, o.DeploymentID, o.Type, "queued", o.Payload, o.CreatedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if e != nil && isConstraint(e) {
		return domain.ErrConflict
	}
	return e
}
func scanOperation(row interface{ Scan(...any) error }) (domain.Operation, error) {
	var o domain.Operation
	var c, u string
	e := row.Scan(&o.ID, &o.DeploymentID, &o.Type, &o.Status, &o.Payload, &o.Result, &o.ErrorCode, &o.ErrorMessage, &c, &u)
	if errors.Is(e, sql.ErrNoRows) {
		return o, domain.ErrNotFound
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	o.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	return o, e
}
func (s *SQLite) GetOperation(ctx context.Context, id string) (domain.Operation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, `SELECT operation_id,deployment_id,type,status,payload,result,error_code,error_message,created_at,updated_at FROM operations WHERE operation_id=?`, id))
}
func (s *SQLite) ClaimOperation(ctx context.Context) (domain.Operation, bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return domain.Operation{}, false, e
	}
	defer func() { _ = tx.Rollback() }()
	o, e := scanOperation(tx.QueryRowContext(ctx, `SELECT operation_id,deployment_id,type,status,payload,result,error_code,error_message,created_at,updated_at FROM operations WHERE status='queued' ORDER BY created_at LIMIT 1`))
	if errors.Is(e, domain.ErrNotFound) {
		return o, false, nil
	}
	if e != nil {
		return o, false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE operations SET status='running',updated_at=? WHERE operation_id=? AND status='queued'`, s.clock.Now().Format(time.RFC3339Nano), o.ID); e != nil {
		return o, false, e
	}
	o.Status = "running"
	if e = tx.Commit(); e != nil {
		return o, false, e
	}
	return o, true, nil
}
func (s *SQLite) CompleteOperation(ctx context.Context, id string, result []byte) error {
	r, e := s.db.ExecContext(ctx, `UPDATE operations SET status='succeeded',result=?,updated_at=? WHERE operation_id=?`, result, s.clock.Now().Format(time.RFC3339Nano), id)
	if e == nil {
		n, _ := r.RowsAffected()
		if n == 0 {
			return domain.ErrNotFound
		}
	}
	return e
}
func (s *SQLite) FailOperation(ctx context.Context, id, code, message string) error {
	_, e := s.db.ExecContext(ctx, `UPDATE operations SET status='failed',error_code=?,error_message=?,updated_at=? WHERE operation_id=?`, code, message, s.clock.Now().Format(time.RFC3339Nano), id)
	return e
}
func (s *SQLite) RecordStep(ctx context.Context, id, step, status, detail string) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO operation_steps(operation_id,step,status,detail,created_at)VALUES(?,?,?,?,?)`, id, step, status, detail, s.clock.Now().Format(time.RFC3339Nano))
	return e
}
func (s *SQLite) GetIdempotency(ctx context.Context, key string) ([]byte, string, bool, error) {
	var response []byte
	var hash string
	e := s.db.QueryRowContext(ctx, `SELECT response,request_hash FROM idempotency_keys WHERE key=?`, key).Scan(&response, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	return response, hash, e == nil, e
}
func (s *SQLite) PutIdempotency(ctx context.Context, key, hash string, response []byte) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO idempotency_keys(key,request_hash,response,created_at)VALUES(?,?,?,?)`, key, hash, response, s.clock.Now().Format(time.RFC3339Nano))
	if e != nil && isConstraint(e) {
		return domain.ErrConflict
	}
	return e
}
func (s *SQLite) CreatePurgeToken(ctx context.Context, id string, hash []byte, expires time.Time) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO purge_tokens(deployment_id,token_hash,expires_at)VALUES(?,?,?)`, id, hash, expires.Format(time.RFC3339Nano))
	return e
}
func (s *SQLite) ConsumePurgeToken(ctx context.Context, id string, hash []byte, now time.Time) (bool, error) {
	r, e := s.db.ExecContext(ctx, `UPDATE purge_tokens SET consumed_at=? WHERE deployment_id=? AND token_hash=? AND consumed_at IS NULL AND expires_at>?`, now.Format(time.RFC3339Nano), id, hash, now.Format(time.RFC3339Nano))
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n == 1, e
}
func isConstraint(e error) bool {
	return e != nil && (contains(e.Error(), "constraint failed") || contains(e.Error(), "UNIQUE constraint"))
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ = fmt.Sprintf
