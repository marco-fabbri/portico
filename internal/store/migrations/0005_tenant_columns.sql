-- Sites (Milestone 4): the Prism Central identifiers live on the
-- placements since 0004; nothing reads them on the tenant row any more.
ALTER TABLE tenants DROP COLUMN project_id;
ALTER TABLE tenants DROP COLUMN project_ext_id;
ALTER TABLE tenants DROP COLUMN resource_group_ext_id;
ALTER TABLE tenants DROP COLUMN saml_group_ext_id;
ALTER TABLE tenants DROP COLUMN role_membership_ext_id;
ALTER TABLE tenants DROP COLUMN idp_shared;
