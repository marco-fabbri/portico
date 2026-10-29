-- Tenant ready network (0.2.0, tenant networking design 4.2 and 4.3): the
-- tenant's /24 and floating IP allowance on the tenant row, the ready
-- network's own state and the objects Portico created on each placement.
ALTER TABLE tenants ADD COLUMN subnet TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN subnet_exception INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tenants ADD COLUMN fip_allowance INTEGER NOT NULL DEFAULT 1;

ALTER TABLE tenant_placements ADD COLUMN net_state TEXT NOT NULL DEFAULT 'none';
ALTER TABLE tenant_placements ADD COLUMN net_error TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN transit_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN ext_subnet_ext_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN vpc_ext_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN subnet_ext_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN floating_ips TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_placements ADD COLUMN snat_ip TEXT NOT NULL DEFAULT '';
