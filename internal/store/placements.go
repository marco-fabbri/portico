package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LegacySiteID is the site a single-Prism Central store becomes (design
// sites §4.7): migration 0004 places every existing tenant on it.
const LegacySiteID = "site-1"

// PlacementStatus is the lifecycle state of a tenant on one site.
type PlacementStatus string

const (
	PlacementCreating PlacementStatus = "creating"
	PlacementActive   PlacementStatus = "active"
	PlacementFailed   PlacementStatus = "failed"
	PlacementRemoving PlacementStatus = "removing"
)

// Placement is a tenant on one site: the Projects 2.0 objects Portico
// created in that site's Prism Central (design §4.4, sites §4.2).
type Placement struct {
	TenantID, SiteID                            string
	Status                                      PlacementStatus
	ClusterExtID, StorageContainerExtID         string
	ProjectID, ProjectExtID, ResourceGroupExtID string
	SAMLGroupExtID, RoleMembershipExtID         string
	IdPShared                                   bool
	LastError                                   string
	CreatedAt, UpdatedAt                        time.Time
}

const placementColumns = `tenant_id, site_id, status, cluster_ext_id, storage_container_ext_id, project_id, project_ext_id,
	resource_group_ext_id, saml_group_ext_id, role_membership_ext_id, idp_shared, last_error, created_at, updated_at`

func scanPlacement(row interface{ Scan(...any) error }) (*Placement, error) {
	var p Placement
	var shared int
	var created, updated string
	err := row.Scan(&p.TenantID, &p.SiteID, &p.Status, &p.ClusterExtID, &p.StorageContainerExtID, &p.ProjectID, &p.ProjectExtID,
		&p.ResourceGroupExtID, &p.SAMLGroupExtID, &p.RoleMembershipExtID, &shared, &p.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}
	p.IdPShared = shared != 0
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return &p, nil
}

// CreatePlacement inserts p and sets its timestamps; ErrExists when the
// tenant already has a placement on that site.
func (s *SQLite) CreatePlacement(ctx context.Context, p *Placement) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_placements (`+placementColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.TenantID, p.SiteID, string(p.Status), p.ClusterExtID, p.StorageContainerExtID, p.ProjectID, p.ProjectExtID,
		p.ResourceGroupExtID, p.SAMLGroupExtID, p.RoleMembershipExtID, boolInt(p.IdPShared), p.LastError,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if isUniqueViolation(err) {
		return ErrExists
	}
	if err == nil {
		s.chmodFiles()
	}
	return err
}

// GetPlacement returns the tenant's placement on the site.
func (s *SQLite) GetPlacement(ctx context.Context, tenantID, siteID string) (*Placement, error) {
	p, err := scanPlacement(s.db.QueryRowContext(ctx, `SELECT `+placementColumns+` FROM tenant_placements WHERE tenant_id = ? AND site_id = ?`, tenantID, siteID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *SQLite) listPlacements(ctx context.Context, where, order string, arg string) ([]Placement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+placementColumns+` FROM tenant_placements WHERE `+where+` ORDER BY `+order, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Placement
	for rows.Next() {
		p, err := scanPlacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListPlacements returns the tenant's placements in creation order.
func (s *SQLite) ListPlacements(ctx context.Context, tenantID string) ([]Placement, error) {
	return s.listPlacements(ctx, "tenant_id = ?", "created_at, site_id", tenantID)
}

// ListPlacementsBySite returns every placement on the site, by tenant.
func (s *SQLite) ListPlacementsBySite(ctx context.Context, siteID string) ([]Placement, error) {
	return s.listPlacements(ctx, "site_id = ?", "tenant_id", siteID)
}

// UpdatePlacement writes every field of p and bumps UpdatedAt.
func (s *SQLite) UpdatePlacement(ctx context.Context, p *Placement) error {
	p.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	res, err := s.db.ExecContext(ctx, `UPDATE tenant_placements SET status=?, cluster_ext_id=?, storage_container_ext_id=?,
		project_id=?, project_ext_id=?, resource_group_ext_id=?, saml_group_ext_id=?, role_membership_ext_id=?, idp_shared=?,
		last_error=?, updated_at=? WHERE tenant_id = ? AND site_id = ?`,
		string(p.Status), p.ClusterExtID, p.StorageContainerExtID, p.ProjectID, p.ProjectExtID, p.ResourceGroupExtID,
		p.SAMLGroupExtID, p.RoleMembershipExtID, boolInt(p.IdPShared), p.LastError, formatTime(p.UpdatedAt), p.TenantID, p.SiteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlacement removes the tenant's placement on the site.
func (s *SQLite) DeletePlacement(ctx context.Context, tenantID, siteID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tenant_placements WHERE tenant_id = ? AND site_id = ?`, tenantID, siteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlacementsBySite removes every placement on the site (a site
// forgotten, sites §4.1) and returns how many there were.
func (s *SQLite) DeletePlacementsBySite(ctx context.Context, siteID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tenant_placements WHERE site_id = ?`, siteID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
