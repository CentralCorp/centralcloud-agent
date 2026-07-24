package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/centralcorp/centralcloud-node-agent/internal/config"
	"github.com/centralcorp/centralcloud-node-agent/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Provisioner struct{ pool *pgxpool.Pool }

func New(ctx context.Context, c config.Config) (*Provisioner, error) {
	b, e := os.ReadFile(c.Postgres.AdministratorPasswordFile)
	if e != nil {
		return nil, fmt.Errorf("read postgres password: %w", e)
	}
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=prefer", c.Postgres.Host, c.Postgres.Port, c.Postgres.AdministratorDatabase, c.Postgres.AdministratorUsername, strings.TrimSpace(string(b)))
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return nil, e
	}
	return &Provisioner{pool: pool}, nil
}
func (p *Provisioner) Close()                         { p.pool.Close() }
func (p *Provisioner) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Provisioner) EnsureRoleAndDatabase(ctx context.Context, db, user, password, marker string) (err error) {
	if e := domain.ValidateDatabaseIdentifier(db); e != nil {
		return e
	}
	if e := domain.ValidateDatabaseIdentifier(user); e != nil {
		return e
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var exists bool
	if e := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, user).Scan(&exists); e != nil {
		return e
	}
	var q string
	if !exists {
		if e := conn.QueryRow(ctx, `SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', $1::text, $2::text)`, user, password).Scan(&q); e != nil {
			return e
		}
		if _, e := conn.Exec(ctx, q); e != nil {
			return e
		}
	} else {
		var comment *string
		var superuser, createDatabase, createRole, replication, bypassRLS bool
		if e := conn.QueryRow(ctx, `
			SELECT shobj_description(oid,'pg_authid'), rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
			FROM pg_roles
			WHERE rolname=$1
		`, user).Scan(&comment, &superuser, &createDatabase, &createRole, &replication, &bypassRLS); e != nil {
			return e
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("role %s exists but is not owned by deployment", user)
		}
		if superuser || createDatabase || createRole || replication || bypassRLS {
			return fmt.Errorf("role %s has unsafe elevated attributes", user)
		}
		if e := conn.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L LOGIN NOINHERIT', $1::text, $2::text)`, user, password).Scan(&q); e != nil {
			return e
		}
		if _, e := conn.Exec(ctx, q); e != nil {
			return e
		}
	}
	if e := conn.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L', $1::text, $2::text)`, user, "centralcloud:"+marker).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	var administrator string
	if e := conn.QueryRow(ctx, `SELECT session_user`).Scan(&administrator); e != nil {
		return e
	}
	if e := p.setRoleOption(ctx, conn, user, administrator, true); e != nil {
		return fmt.Errorf("enable database owner SET ROLE: %w", e)
	}
	roleActive := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if roleActive {
			if _, cleanupErr := conn.Exec(cleanupCtx, `RESET ROLE`); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("reset database owner role: %w", cleanupErr))
				return
			}
		}
		if cleanupErr := p.setRoleOption(cleanupCtx, conn, user, administrator, false); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("restore database owner SET ROLE restriction: %w", cleanupErr))
		}
	}()
	if e := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, db).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		if e := conn.QueryRow(ctx, `SELECT format('CREATE DATABASE %I OWNER %I', $1::text, $2::text)`, db, user).Scan(&q); e != nil {
			return e
		}
		if _, e := conn.Exec(ctx, q); e != nil {
			return e
		}
	} else {
		var owner string
		var comment *string
		if e := conn.QueryRow(ctx, `SELECT pg_get_userbyid(datdba), shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, db).Scan(&owner, &comment); e != nil {
			return e
		}
		if owner != user || comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("database %s exists but is not owned by deployment", db)
		}
	}
	if e := conn.QueryRow(ctx, `SELECT format('SET ROLE %I', $1::text)`, user).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	roleActive = true
	if e := conn.QueryRow(ctx, `SELECT format('COMMENT ON DATABASE %I IS %L', $1::text, $2::text)`, db, "centralcloud:"+marker).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	if e := conn.QueryRow(ctx, `SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', $1::text)`, db).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	if e := conn.QueryRow(ctx, `SELECT format('GRANT ALL ON DATABASE %I TO %I', $1::text, $2::text)`, db, user).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, `RESET ROLE`); e != nil {
		return e
	}
	roleActive = false
	return nil
}

func (p *Provisioner) setRoleOption(ctx context.Context, conn *pgxpool.Conn, role, member string, enabled bool) error {
	formatQuery := `SELECT format('GRANT %I TO %I WITH SET FALSE, INHERIT FALSE', $1::text, $2::text)`
	if enabled {
		formatQuery = `SELECT format('GRANT %I TO %I WITH SET TRUE, INHERIT TRUE', $1::text, $2::text)`
	}
	var query string
	if err := conn.QueryRow(ctx, formatQuery, role, member).Scan(&query); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, query)
	return err
}

func (p *Provisioner) DropRoleAndDatabase(ctx context.Context, db, user, marker string) (err error) {
	if e := domain.ValidateDatabaseIdentifier(db); e != nil {
		return e
	}
	if e := domain.ValidateDatabaseIdentifier(user); e != nil {
		return e
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var roleExists bool
	if e := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, user).Scan(&roleExists); e != nil {
		return e
	}
	if roleExists {
		var comment *string
		if e := conn.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, user).Scan(&comment); e != nil {
			return fmt.Errorf("verify managed role: %w", e)
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("refusing to drop unowned role")
		}
	}
	var dbExists bool
	if e := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, db).Scan(&dbExists); e != nil {
		return e
	}
	if dbExists {
		var comment *string
		if e := conn.QueryRow(ctx, `SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, db).Scan(&comment); e != nil {
			return e
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("refusing to drop unowned database")
		}
	}
	membershipElevated := false
	if roleExists {
		var administrator string
		if e := conn.QueryRow(ctx, `SELECT session_user`).Scan(&administrator); e != nil {
			return e
		}
		if e := p.setRoleOption(ctx, conn, user, administrator, true); e != nil {
			return fmt.Errorf("enable database owner privileges for purge: %w", e)
		}
		membershipElevated = true
		defer func() {
			if !membershipElevated {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if cleanupErr := p.setRoleOption(cleanupCtx, conn, user, administrator, false); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("restore database owner restrictions after purge: %w", cleanupErr))
			}
		}()
	}
	var q string
	if e := conn.QueryRow(ctx, `SELECT format('DROP DATABASE IF EXISTS %I WITH (FORCE)', $1::text)`, db).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	if e := conn.QueryRow(ctx, `SELECT format('DROP ROLE IF EXISTS %I', $1::text)`, user).Scan(&q); e != nil {
		return e
	}
	if _, e := conn.Exec(ctx, q); e != nil {
		return e
	}
	membershipElevated = false
	return nil
}
