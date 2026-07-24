package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureRoleAndDatabasePostgres17(t *testing.T) {
	dsn := os.Getenv("CENTRALCLOUD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("CENTRALCLOUD_POSTGRES_TEST_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	database := fmt.Sprintf("cc_test_db_%d", suffix)
	role := fmt.Sprintf("cc_test_role_%d", suffix)
	marker := fmt.Sprintf("cc-test-%d", suffix)
	provisioner := &Provisioner{pool: pool}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provisioner.DropRoleAndDatabase(cleanupCtx, database, role, marker); err != nil {
			t.Errorf("cleanup test database and role: %v", err)
		}
	})

	if err := provisioner.EnsureRoleAndDatabase(ctx, database, role, "test-password-not-secret", marker); err != nil {
		t.Fatalf("ensure role and database: %v", err)
	}

	var owner string
	var comment *string
	if err := pool.QueryRow(ctx, `SELECT pg_get_userbyid(datdba), shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, database).Scan(&owner, &comment); err != nil {
		t.Fatal(err)
	}
	if owner != role {
		t.Fatalf("database owner = %q, want %q", owner, role)
	}
	if comment == nil || *comment != "centralcloud:"+marker {
		t.Fatalf("database marker = %v, want centralcloud:%s", comment, marker)
	}

	var adminOption, inheritOption, setOption bool
	if err := pool.QueryRow(ctx, `
		SELECT membership.admin_option, membership.inherit_option, membership.set_option
		FROM pg_auth_members membership
		JOIN pg_roles granted_role ON granted_role.oid = membership.roleid
		JOIN pg_roles member_role ON member_role.oid = membership.member
		WHERE granted_role.rolname=$1 AND member_role.rolname=session_user
	`, role).Scan(&adminOption, &inheritOption, &setOption); err != nil {
		t.Fatal(err)
	}
	if !adminOption || inheritOption || setOption {
		t.Fatalf("membership options = admin:%t inherit:%t set:%t, want admin:true inherit:false set:false", adminOption, inheritOption, setOption)
	}

	if err := provisioner.EnsureRoleAndDatabase(ctx, database, role, "rotated-test-password-not-secret", marker); err != nil {
		t.Fatalf("repeat ensure role and database: %v", err)
	}
}
