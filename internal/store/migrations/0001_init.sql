-- Portico store, first schema. Timestamps are RFC 3339 UTC text.
CREATE TABLE tenants (
    id                     TEXT PRIMARY KEY,
    display_name           TEXT NOT NULL DEFAULT '',
    contacts               TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL,
    project_id             TEXT NOT NULL DEFAULT '',
    project_ext_id         TEXT NOT NULL DEFAULT '',
    resource_group_ext_id  TEXT NOT NULL DEFAULT '',
    saml_group_ext_id      TEXT NOT NULL DEFAULT '',
    keycloak_group_id      TEXT NOT NULL DEFAULT '',
    role_membership_ext_id TEXT NOT NULL DEFAULT '',
    idp_shared             INTEGER NOT NULL DEFAULT 0,
    last_error             TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

CREATE TABLE tenant_steps (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name      TEXT NOT NULL,
    ok        INTEGER NOT NULL,
    detail    TEXT NOT NULL DEFAULT '',
    at        TEXT NOT NULL
);
CREATE INDEX tenant_steps_tenant ON tenant_steps(tenant_id, seq);

CREATE TABLE admin_accounts (
    name          TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
