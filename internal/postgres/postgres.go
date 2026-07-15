package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"

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
func (p *Provisioner) EnsureRoleAndDatabase(ctx context.Context, db, user, password, marker string) error {
	if e := domain.ValidateDatabaseIdentifier(db); e != nil {
		return e
	}
	if e := domain.ValidateDatabaseIdentifier(user); e != nil {
		return e
	}
	var exists bool
	if e := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, user).Scan(&exists); e != nil {
		return e
	}
	var q string
	if !exists {
		if e := p.pool.QueryRow(ctx, `SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', $1::text, $2::text)`, user, password).Scan(&q); e != nil {
			return e
		}
		if _, e := p.pool.Exec(ctx, q); e != nil {
			return e
		}
	} else {
		var comment *string
		if e := p.pool.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, user).Scan(&comment); e != nil {
			return e
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("role %s exists but is not owned by deployment", user)
		}
		if e := p.pool.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', $1::text, $2::text)`, user, password).Scan(&q); e != nil {
			return e
		}
		if _, e := p.pool.Exec(ctx, q); e != nil {
			return e
		}
	}
	if e := p.pool.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L', $1::text, $2::text)`, user, "centralcloud:"+marker).Scan(&q); e != nil {
		return e
	}
	if _, e := p.pool.Exec(ctx, q); e != nil {
		return e
	}
	if e := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, db).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		if e := p.pool.QueryRow(ctx, `SELECT format('CREATE DATABASE %I OWNER %I', $1::text, $2::text)`, db, user).Scan(&q); e != nil {
			return e
		}
		if _, e := p.pool.Exec(ctx, q); e != nil {
			return e
		}
	} else {
		var owner string
		var comment *string
		if e := p.pool.QueryRow(ctx, `SELECT pg_get_userbyid(datdba), shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, db).Scan(&owner, &comment); e != nil {
			return e
		}
		if owner != user || comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("database %s exists but is not owned by deployment", db)
		}
	}
	if e := p.pool.QueryRow(ctx, `SELECT format('COMMENT ON DATABASE %I IS %L', $1::text, $2::text)`, db, "centralcloud:"+marker).Scan(&q); e != nil {
		return e
	}
	if _, e := p.pool.Exec(ctx, q); e != nil {
		return e
	}
	if e := p.pool.QueryRow(ctx, `SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', $1::text)`, db).Scan(&q); e != nil {
		return e
	}
	if _, e := p.pool.Exec(ctx, q); e != nil {
		return e
	}
	if e := p.pool.QueryRow(ctx, `SELECT format('GRANT ALL ON DATABASE %I TO %I', $1::text, $2::text)`, db, user).Scan(&q); e != nil {
		return e
	}
	_, e := p.pool.Exec(ctx, q)
	return e
}
func (p *Provisioner) DropRoleAndDatabase(ctx context.Context, db, user, marker string) error {
	if e := domain.ValidateDatabaseIdentifier(db); e != nil {
		return e
	}
	if e := domain.ValidateDatabaseIdentifier(user); e != nil {
		return e
	}
	var roleExists bool
	if e := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, user).Scan(&roleExists); e != nil {
		return e
	}
	if roleExists {
		var comment *string
		if e := p.pool.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, user).Scan(&comment); e != nil {
			return fmt.Errorf("verify managed role: %w", e)
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("refusing to drop unowned role")
		}
	}
	var dbExists bool
	if e := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, db).Scan(&dbExists); e != nil {
		return e
	}
	if dbExists {
		var comment *string
		if e := p.pool.QueryRow(ctx, `SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, db).Scan(&comment); e != nil {
			return e
		}
		if comment == nil || *comment != "centralcloud:"+marker {
			return fmt.Errorf("refusing to drop unowned database")
		}
	}
	var q string
	if e := p.pool.QueryRow(ctx, `SELECT format('DROP DATABASE IF EXISTS %I WITH (FORCE)', $1::text)`, db).Scan(&q); e != nil {
		return e
	}
	if _, e := p.pool.Exec(ctx, q); e != nil {
		return e
	}
	if e := p.pool.QueryRow(ctx, `SELECT format('DROP ROLE IF EXISTS %I', $1::text)`, user).Scan(&q); e != nil {
		return e
	}
	_, e := p.pool.Exec(ctx, q)
	return e
}
