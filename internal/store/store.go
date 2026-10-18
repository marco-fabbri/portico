// Package store is Portico's own persistent state: tenants, the record of
// their provisioning steps, and the break-glass administrator account.
//
// Store is the interface every consumer uses; SQLite is the implementation
// for the single-node appliance. The pair topology (design §4.11) adds a
// PostgreSQL implementation behind the same interface. Portico's state is a
// record, not the truth: Prism Central and Keycloak own the objects, and
// the rows can be rebuilt from them.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: the binary is built with CGO_ENABLED=0
)

var (
	// ErrNotFound is returned when the row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrExists is returned when a row with the same key exists.
	ErrExists = errors.New("store: already exists")
)

// Status is the lifecycle state of a tenant.
type Status string

const (
	StatusCreating Status = "creating"
	StatusActive   Status = "active"
	StatusFailed   Status = "failed"
	StatusDisabled Status = "disabled"
	StatusDeleting Status = "deleting"
)

// Op names the operation recorded in Tenant.LastOp.
const (
	OpProvision = "provision"
	OpDelete    = "delete"
)

// Tenant ties a Keycloak group to a Projects 2.0 project (design §4.4).
type Tenant struct {
	ID, DisplayName, Contacts                            string
	Status                                               Status
	ProjectID, ProjectExtID, ResourceGroupExtID          string
	SAMLGroupExtID, KeycloakGroupID, RoleMembershipExtID string
	IdPShared                                            bool
	LastError                                            string
	LastOp                                               string // OpProvision or OpDelete
	CreatedAt, UpdatedAt                                 time.Time
}

// Step is one recorded provisioning or deletion step of a tenant.
type Step struct {
	TenantID, Name string
	OK             bool
	Detail         string
	At             time.Time
}

// Store is the interface every consumer (tenant service, API, serve) uses.
type Store interface {
	Close() error
	CreateTenant(ctx context.Context, t *Tenant) error
	GetTenant(ctx context.Context, id string) (*Tenant, error)
	ListTenants(ctx context.Context) ([]Tenant, error)
	UpdateTenant(ctx context.Context, t *Tenant) error
	DeleteTenant(ctx context.Context, id string) error
	AddStep(ctx context.Context, st Step) error
	ListSteps(ctx context.Context, tenantID string) ([]Step, error)
	SetAdminPassword(ctx context.Context, name, bcryptHash string) error
	AdminPasswordHash(ctx context.Context, name string) (string, error)
}

//go:embed migrations/*.sql
var migrations embed.FS

// SQLite implements Store on one SQLite file in WAL mode.
type SQLite struct {
	db *sql.DB
}

var _ Store = (*SQLite)(nil)

// OpenSQLite opens or creates the database at path and applies the
// migrations that are not yet applied.
func OpenSQLite(path string) (*SQLite, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection: SQLite serialises writers anyway, and a single
	// connection keeps the pragmas and transactions simple.
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", name)
		}
		if version <= current {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
