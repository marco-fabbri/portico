package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const tenantColumns = `id, display_name, contacts, status, project_id, project_ext_id, resource_group_ext_id,
	saml_group_ext_id, keycloak_group_id, role_membership_ext_id, idp_shared, last_error, last_op, created_at, updated_at`

func scanTenant(row interface{ Scan(...any) error }) (*Tenant, error) {
	var t Tenant
	var shared int
	var created, updated string
	err := row.Scan(&t.ID, &t.DisplayName, &t.Contacts, &t.Status, &t.ProjectID, &t.ProjectExtID, &t.ResourceGroupExtID,
		&t.SAMLGroupExtID, &t.KeycloakGroupID, &t.RoleMembershipExtID, &shared, &t.LastError, &t.LastOp, &created, &updated)
	if err != nil {
		return nil, err
	}
	t.IdPShared = shared != 0
	t.CreatedAt, t.UpdatedAt = parseTime(created), parseTime(updated)
	return &t, nil
}

// CreateTenant inserts t and sets its timestamps.
func (s *SQLite) CreateTenant(ctx context.Context, t *Tenant) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenants (`+tenantColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.DisplayName, t.Contacts, string(t.Status), t.ProjectID, t.ProjectExtID, t.ResourceGroupExtID,
		t.SAMLGroupExtID, t.KeycloakGroupID, t.RoleMembershipExtID, boolInt(t.IdPShared), t.LastError, t.LastOp,
		formatTime(t.CreatedAt), formatTime(t.UpdatedAt))
	if isUniqueViolation(err) {
		return ErrExists
	}
	if err == nil {
		s.chmodFiles()
	}
	return err
}

// GetTenant returns the tenant with the given ID.
func (s *SQLite) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	t, err := scanTenant(s.db.QueryRowContext(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ListTenants returns every tenant ordered by ID.
func (s *SQLite) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tenantColumns+` FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UpdateTenant writes every field of t and bumps UpdatedAt.
func (s *SQLite) UpdateTenant(ctx context.Context, t *Tenant) error {
	t.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	res, err := s.db.ExecContext(ctx, `UPDATE tenants SET display_name=?, contacts=?, status=?, project_id=?, project_ext_id=?,
		resource_group_ext_id=?, saml_group_ext_id=?, keycloak_group_id=?, role_membership_ext_id=?, idp_shared=?, last_error=?, last_op=?, updated_at=?
		WHERE id = ?`,
		t.DisplayName, t.Contacts, string(t.Status), t.ProjectID, t.ProjectExtID, t.ResourceGroupExtID, t.SAMLGroupExtID,
		t.KeycloakGroupID, t.RoleMembershipExtID, boolInt(t.IdPShared), t.LastError, t.LastOp, formatTime(t.UpdatedAt), t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTenant removes the tenant and, by cascade, its steps.
func (s *SQLite) DeleteTenant(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddStep appends a step record for a tenant.
func (s *SQLite) AddStep(ctx context.Context, st Step) error {
	if st.At.IsZero() {
		st.At = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_steps (tenant_id, name, ok, detail, at) VALUES (?,?,?,?,?)`,
		st.TenantID, st.Name, boolInt(st.OK), st.Detail, formatTime(st.At))
	return err
}

// ListSteps returns the steps of a tenant in insertion order.
func (s *SQLite) ListSteps(ctx context.Context, tenantID string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, name, ok, detail, at FROM tenant_steps WHERE tenant_id = ? ORDER BY seq`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var st Step
		var ok int
		var at string
		if err := rows.Scan(&st.TenantID, &st.Name, &ok, &st.Detail, &at); err != nil {
			return nil, err
		}
		st.OK, st.At = ok != 0, parseTime(at)
		out = append(out, st)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
