-- Sites (Milestone 4): a tenant has one placement per site where it has
-- resources. The Prism Central identifiers move from the tenant row to
-- the placement; the old columns are dropped in 0005 once nothing reads
-- them. A store written by a single-Prism Central release becomes site
-- 'site-1' (the settings document is moved the same way at load).
CREATE TABLE tenant_placements (
    tenant_id                TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id                  TEXT NOT NULL,
    status                   TEXT NOT NULL,
    cluster_ext_id           TEXT NOT NULL DEFAULT '',
    storage_container_ext_id TEXT NOT NULL DEFAULT '',
    project_id               TEXT NOT NULL DEFAULT '',
    project_ext_id           TEXT NOT NULL DEFAULT '',
    resource_group_ext_id    TEXT NOT NULL DEFAULT '',
    saml_group_ext_id        TEXT NOT NULL DEFAULT '',
    role_membership_ext_id   TEXT NOT NULL DEFAULT '',
    idp_shared               INTEGER NOT NULL DEFAULT 0,
    last_error               TEXT NOT NULL DEFAULT '',
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    PRIMARY KEY (tenant_id, site_id)
);
CREATE INDEX tenant_placements_site ON tenant_placements(site_id, tenant_id);

ALTER TABLE tenant_steps ADD COLUMN site_id TEXT NOT NULL DEFAULT '';

INSERT INTO tenant_placements (tenant_id, site_id, status, cluster_ext_id, storage_container_ext_id,
    project_id, project_ext_id, resource_group_ext_id, saml_group_ext_id, role_membership_ext_id,
    idp_shared, last_error, created_at, updated_at)
SELECT t.id, 'site-1',
    CASE t.status WHEN 'active' THEN 'active' WHEN 'disabled' THEN 'active' WHEN 'creating' THEN 'creating' ELSE 'failed' END,
    COALESCE((SELECT json_extract(CAST(value AS TEXT), '$.tenants.cluster_ext_id') FROM settings WHERE key = 'settings'), ''),
    COALESCE((SELECT json_extract(CAST(value AS TEXT), '$.tenants.storage_container_ext_id') FROM settings WHERE key = 'settings'), ''),
    t.project_id, t.project_ext_id, t.resource_group_ext_id, t.saml_group_ext_id, t.role_membership_ext_id,
    t.idp_shared, CASE WHEN t.status IN ('failed', 'deleting') THEN t.last_error ELSE '' END,
    t.created_at, t.updated_at
FROM tenants t
WHERE t.project_id <> '';

UPDATE tenant_steps SET site_id = 'site-1' WHERE name <> 'keycloak-group';
