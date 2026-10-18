-- Which operation last ran on the tenant ('provision' or 'delete'): a
-- retry must never re-provision a tenant whose deletion failed.
ALTER TABLE tenants ADD COLUMN last_op TEXT NOT NULL DEFAULT '';
