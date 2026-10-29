# Tenant Ready Network (0.2.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connecting a tenant to a site whose ready network is on gives it,
in its project, a VPC attached to its own overlay external subnet in the
MSP's routed transit VPC, one internal `/24`, a default route and its
floating IPs; Disconnect removes them, whatever the other tenants hold.

**Architecture:** A networking part of the Prism Central client
(`internal/nutanix/networking.go`), pure address arithmetic
(`internal/netplan`), readiness checks over a small reader interface
(`internal/netready`), seven recorded steps run by the tenant service after
the existing connection steps with a state of their own, the two roles
created by the site setup run, and the site's Networking card. The ready
network never changes the connection's own status: a tenant signs in as in
0.1.0 even while its network is building or failed.

**Tech Stack:** Go 1.27 standard library (`net/netip`), the existing
packages and fakes (`internal/tenant/tenanttest`, `internal/nutanix`'s
`fakePC`, `internal/settings/settingstest`), HTMX templates. Lab: NTNX-01
with `external-routed`, `portico-transit` (`10.23.210.200`, ERP
`10.23.211.0/24`) and the appliance `.191`.

**Spec:** `docs/superpowers/specs/2026-10-29-tenant-network-design.md`
(revised in PR #59); measurements in
`docs/lab/2026-10-29-34-flow-virtual-networking.md`.

## Global Constraints

- All text on disk in English; Italian only in chat with Marco.
- Commits: `git -c user.email=9093189+marco-fabbri@users.noreply.github.com commit`,
  message ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
  PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
  Merge only with Marco's OK: `gh pr merge N --rebase --delete-branch`.
- `go test ./...`, `go vet ./...`, `gofmt -l cmd internal` clean, and
  `lab/test_scripts.sh` PASS before every commit that touches them.
- Never log, print or return a password, API key, client secret, token,
  session id, CSRF token or cookie value. The one-time administrator
  credentials of a setup run are never stored.
- Networking API `/api/networking/v4.4/config`; every create, update and
  delete is a task waited with `WaitTask`; updates and deletes carry the
  entity's ETag in `If-Match`.
- Names: `<project>-ext`, `<project>-vpc`, `<project>-net`,
  `<project>-fip-<n>` (n from 1); the tenant's objects carry its
  `projectExtId`, `<project>-ext` carries none.
- Portico never changes the transit VPC, its VLAN external subnet, `MGMT`
  or another tenant's objects, and never deletes a VM, a NIC or a floating
  IP the tenant made itself.
- Prism Central assigns addresses in no fixed order: read them back,
  never predict them.

## Review Focus

1. A subnet created with `projectExtId` that comes back without it
   (measured once in three) must be deleted and created once more, then
   fail the step with that reason, never be used: Task 6 asserts both
   outcomes.
2. Two tenants on one site get two different `/28`s of the routed prefix,
   also when the first one's connection failed half way and holds its
   `/28` without a VPC; a `/28` returns to the free list only when its
   connection is gone: Task 2 and Task 6 assert it.
3. Disconnect of a tenant whose floating IP the tenant associated with a
   VM must refuse with the VM named, not delete the floating IP (Prism
   Central would, measured): Task 7 asserts it.
4. A site set up before 0.2.0 (no Portico Tenant role yet) must keep
   connecting tenants with Project Admin and say "Repair the site" on its
   Networking card, not fail every Connect: Task 5 asserts it.
5. An allowance lowered below the number of associated floating IPs must
   delete only unassociated ones and say how many it kept: Task 8 asserts
   it.

---

### Task 0: The measurements still open (lab, before Task 5)

Spec section 6 items 1 and 3; results go into write-up 34 and, when they
change a value of this plan, into a ledger ruling.

- [ ] **Step 1:** Marco runs `lab/auth/networking-role.sh create` (the role of Task 5, `SUBNET_SCOPE=all`). With a temporary key of the service account (`lab/auth/create-service-account.sh create`), `PC_API_KEY=… lab/network/transit.sh create` for a tenant's `/28` and `lab/network/tenant-network.sh create|delete`. Expected: every step `SUCCEEDED`. Then the key is revoked.
- [ ] **Step 2:** The same with `SUBNET_SCOPE=external-routed` and a `vpc`-scoped filter, to find out whether a narrower filter works; the answer sets Task 5's entity filters.
- [ ] **Step 3:** A local user with a copy of Project Admin without `Create_Floating_Ip` and `Delete_Floating_Ip` (`lab/projects/probe-user.sh` with `ROLE=`): VM create, floating IP association, and a refused floating IP create. Expected: the first two succeed, the third is 403.
- [ ] **Step 4: Commit** the write-up lines.

### Task 1: Networking client

**Files:**
- Create: `internal/nutanix/networking.go`, `internal/nutanix/networking_test.go`
- Modify: `internal/nutanix/fakepc_test.go` (routes for the new paths)

**Interfaces:**
- Produces (all on `*Client`, path prefix `/api/networking/v4.4/config`):
  - `type Subnet struct { ExtID, Name, Type, ProjectExtID, VPCExtID string; External, NAT bool; Prefix netip.Prefix; SharedWith []string }`
  - `type VPC struct { ExtID, Name, Type, ProjectExtID string; External []VPCExternal; RoutablePrefixes []netip.Prefix }`, `type VPCExternal struct { SubnetExtID string; IPs []netip.Addr }`
  - `type FloatingIP struct { ExtID, Name, ProjectExtID, ExternalSubnetExtID string; IP netip.Addr; AssociatedVMNIC string; Associated bool }`
  - `type Route struct { ExtID string; Destination netip.Prefix; NextHopType, NextHopExtID string }`
  - `ListSubnets(ctx) ([]Subnet, error)`, `GetSubnet(ctx, extID) (*Subnet, error)`
  - `CreateOverlayExternalSubnet(ctx, name, vpcExtID string, p netip.Prefix, gw, first, last netip.Addr) (*Subnet, error)` (body: `subnetType OVERLAY`, `isExternal true`, `isNatEnabled true`, no project)
  - `CreateOverlaySubnet(ctx, name, projectExtID, vpcExtID string, p netip.Prefix, gw, first, last netip.Addr) (*Subnet, error)`
  - `ShareSubnet(ctx, subnetExtID, projectExtID string) error`, `UnshareSubnet(...)` (`POST /subnets/{id}/$actions/share|unshare`, body `{"projectExtId"}`, `If-Match`)
  - `DeleteSubnet(ctx, extID) error`
  - `ListVPCs(ctx) ([]VPC, error)`, `GetVPC(ctx, extID) (*VPC, error)`
  - `CreateVPC(ctx, name, projectExtID, externalSubnetExtID string, dns []netip.Addr) (*VPC, error)` (`vpcType REGULAR`, `commonDhcpOptions.domainNameServers`)
  - `DeleteVPC(ctx, extID) error`
  - `Routes(ctx, vpcExtID) (routeTableExtID string, routes []Route, err error)` (`GET /route-tables?$filter=vpcReference eq '<id>'`, then its routes)
  - `CreateDefaultRoute(ctx, vpcExtID, routeTableExtID, externalSubnetExtID, projectExtID string) error` (body as write-up 34: `name default`, `routeType STATIC`, `0.0.0.0/0`, `nexthopType EXTERNAL_SUBNET`)
  - `ListFloatingIPs(ctx) ([]FloatingIP, error)`, `GetFloatingIP(ctx, extID) (*FloatingIP, error)`
  - `CreateFloatingIP(ctx, name, projectExtID, externalSubnetExtID string) (*FloatingIP, error)`, `DeleteFloatingIP(ctx, extID) error`
  - Each create waits for its task, then reads the entity by name and returns it as Prism Central stored it.

- [ ] **Step 1: Write the failing tests** in `networking_test.go`:
  `TestCreateVPCBody` (the recorded POST body has `vpcType` `REGULAR`,
  `projectExtId`, one `externalSubnets[0].subnetReference`, the DNS in
  `commonDhcpOptions.domainNameServers[*].ipv4.value`);
  `TestCreateOverlayExternalSubnetHasNoProject` (body `isExternal` and
  `isNatEnabled` true, `vpcReference` set, no `projectExtId` key);
  `TestShareSubnetSendsETag` (`If-Match` equals the ETag of the GET);
  `TestRoutesFiltersByVPC` (query `$filter=vpcReference eq 'vpc-1'`);
  `TestCreateDefaultRouteBody` (destination `0.0.0.0`/`0`, next hop type
  and reference, `routeTableReference`);
  `TestFloatingIPAssociation` (a fixture with `associationStatus`
  `ASSOCIATED_TO_VNIC` and `association.vmNicReference` gives
  `Associated` true and the NIC; `NOT_ASSOCIATED` gives false);
  `TestCreateReturnsStoredEntity` (the returned `ProjectExtID` is the one
  the follow-up GET returns, `""` in the fixture, not the requested one).
- [ ] **Step 2: Run** `go test ./internal/nutanix -run 'VPC|Subnet|Route|FloatingIP|StoredEntity'` — Expected: FAIL, undefined.
- [ ] **Step 3: Implement** the functions above with the existing `do`,
  `etagOf`, `WaitTask` and `listAll`; parse prefixes with `netip`.
- [ ] **Step 4: Run** the same command — Expected: PASS; then `go test ./internal/nutanix`.
- [ ] **Step 5: Commit** `feat(nutanix): networking client for tenant ready networks`.

### Task 2: Address plan

**Files:**
- Create: `internal/netplan/netplan.go`, `internal/netplan/netplan_test.go`

**Interfaces:**
- Produces:
  - `NextFree(pool netip.Prefix, bits int, taken []netip.Prefix) (netip.Prefix, error)`: the lowest `/bits` of `pool` that overlaps nothing in `taken`; `ErrPoolFull` when none.
  - `TenantRange(p netip.Prefix) (gw, first, last netip.Addr)`: gateway `.1`, pool `.10` to `.250` (a `/24` only).
  - `TransitRange(p netip.Prefix) (gw, first, last netip.Addr)`: gateway the first address after the network, pool the next one up to the one before broadcast.
  - `CheckPool(p netip.Prefix) error`: private IPv4, prefix length between 8 and 20.
  - `CheckException(p netip.Prefix, others []netip.Prefix) error`: a private IPv4 `/24` overlapping none of `others`.
  - `const TransitBits = 28`, `const MaxAllowance = 12`.

- [ ] **Step 1: Write the failing tests**:
  `TestNextFreeLowest` (`10.100.0.0/16`, taken `10.100.0.0/24` and
  `10.100.2.0/24` → `10.100.1.0/24`); `TestNextFreeSkipsException`
  (an exception `10.100.0.0/24` inside the pool is taken);
  `TestNextFreeFull` (`10.100.0.0/24` with bits 24 and it taken →
  `ErrPoolFull`); `TestNextFreeTransit` (`10.23.211.0/24`, bits 28, taken
  `10.23.211.0/28` → `10.23.211.16/28`); `TestTransitRange`
  (`10.23.211.16/28` → `.17`, `.18`, `.30`, the values measured);
  `TestTenantRange` (`10.100.11.0/24` → `.1`, `.10`, `.250`);
  `TestCheckPool` (`10.100.0.0/16` ok; `8.8.0.0/16`, `10.0.0.0/7`,
  `10.100.0.0/21` refused); `TestCheckException` (`192.168.5.0/24` ok;
  `192.168.5.0/23` and an overlap refused).
- [ ] **Step 2: Run** `go test ./internal/netplan` — Expected: FAIL.
- [ ] **Step 3: Implement** with `netip.Prefix.Overlaps` and address arithmetic on `As4`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(netplan): tenant and transit address plan`.

### Task 3: Store and settings

**Files:**
- Create: `internal/store/migrations/0006_ready_network.sql`
- Modify: `internal/store/store.go` (`Tenant`), `internal/store/placements.go`, `internal/store/tenant.go`, `internal/settings/settings.go`, `internal/settings/sites.go`, their tests

**Interfaces:**
- Produces:
  - `store.Tenant` gains `Subnet string` (a `/24`, `""` until assigned), `SubnetException bool`, `FIPAllowance int` (default 1).
  - `store.NetState` = `none|building|ready|failed`; `store.Placement` gains `NetState NetState`, `NetError string`, `TransitPrefix string` (the `/28`), `ExtSubnetExtID`, `VPCExtID`, `SubnetExtID string`, `FloatingIPs []store.FloatingIP` (stored as JSON), `SNATIP string`; `type FloatingIP struct { ExtID, IP string }`.
  - `settings.Settings.Tenants.AddressPool string` (default `10.100.0.0/16`).
  - `settings.Site.Network` of type `SiteNetwork struct { TransitVPCExtID string; DNS []string; IntegratedConfirmed bool; Enabled bool }` (JSON `network`).
  - `settings.Settings.Tenants.Role` default becomes `Portico Tenant`; a stored `Project Admin` is moved to `Portico Tenant` at load (my call: it was the 0.1.0 default, not a choice).

- [ ] **Step 1: Write the failing tests**: `TestPlacementNetworkRoundTrip`
  (every new field survives `UpdatePlacement`/`GetPlacement`, two
  floating IPs in order); `TestTenantSubnetRoundTrip`;
  `TestMigration0006Defaults` (a 0005 store opens with `net_state` `none`
  and `fip_allowance` 1); `TestSettingsDefaultPool`; `TestSiteNetworkJSON`;
  `TestLoadMovesProjectAdminToPorticoTenant` (and leaves `Custom Role`
  alone).
- [ ] **Step 2: Run** `go test ./internal/store ./internal/settings` — Expected: FAIL.
- [ ] **Step 3: Implement** the migration (`ALTER TABLE ... ADD COLUMN ... DEFAULT`), the columns in `placementColumns` and the scans, the settings fields and defaults.
- [ ] **Step 4: Run** — Expected: PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(store): ready network fields on tenants, connections and sites`.

### Task 4: Readiness checks

**Files:**
- Create: `internal/netready/netready.go`, `internal/netready/netready_test.go`
- Modify: `internal/nutanix/networking.go` (`Controllers`), `internal/nutanix/clusters.go` (`Cluster` gains `Addresses []netip.Addr`: `externalAddress`, `externalDataServiceIp`, each node's `controllerVmIp` and `hostIp`), `internal/nutanix/client.go` or a new `prism.go` (`PrismGateway`)

**Interfaces:**
- Consumes: Task 1 `ListVPCs`, `GetSubnet`, `Routes`; Task 2 `NextFree`; Task 3 `SiteNetwork`.
- Produces:
  - `type Reader interface { Controllers(ctx) ([]nutanix.Controller, error); PrismGateway(ctx) (netip.Addr, netip.Prefix, error); ListClusters(ctx) ([]nutanix.Cluster, error); ListVPCs(ctx) ([]nutanix.VPC, error); GetSubnet(ctx, string) (*nutanix.Subnet, error); Routes(ctx, string) (string, []nutanix.Route, error) }`
  - `type Check struct { Name, Status, Message string }` (`ok|failed|warning|todo`)
  - `Evaluate(ctx, r Reader, n settings.SiteNetwork, used int) []Check`, checks in this order: `flow`, `mode`, `gateway`, `transit`, `transit-valid`, `rights`, `pool`.
  - `Ready(checks []Check) bool`: no `failed` and no `todo`.
  - `TransitChoices(ctx, r Reader) ([]Choice, error)`: VPCs of type `TRANSIT` with their no-NAT VLAN external subnet, address, routed prefix and free `/28` count.
  - `nutanix.Controller{ Status string }` from `GET /api/networking/v4.4/config/controllers`; `PrismGateway` from `GET /api/prism/v4.2/config/domain-managers`, `network.externalNetworks[0].defaultGateway` and its subnet.

- [ ] **Step 1: Write the failing tests** with a fake `Reader`:
  `TestFlowDown` (no controller `UP` → `flow` failed, message "Enable Flow Virtual Networking in Prism Central (Integrated mode)");
  `TestModeNeedsConfirmation` (`IntegratedConfirmed` false → `mode` todo);
  `TestGatewayIsClusterAddress` (gateway `10.23.210.183` equal to a
  cluster's Data Services IP → failed, message names the address, as NYC1
  had); `TestGatewayOutsideSubnet` → failed; `TestGatewayOK` (`.254`);
  `TestTransitNotPicked` → todo "Pick the transit VPC tenants use";
  `TestTransitInvalid` (type `REGULAR`; a NAT external subnet; no
  `0.0.0.0/0` route; no routable prefix: each failed with its reason);
  `TestPoolWarning` (13 of 16 `/28`s used → warning, still `Ready`);
  `TestRightsMissing` (the reader reports missing operations → failed
  "Repair");
  `TestTransitChoices` (one `TRANSIT`, one `REGULAR` → one choice with
  `10.23.210.200`, `10.23.211.0/24`, 16 free).
- [ ] **Step 2: Run** `go test ./internal/netready ./internal/nutanix` — Expected: FAIL.
- [ ] **Step 3: Implement.** `rights` reads the service account's missing
  operations through `MissingOperations` with the list of Task 5; the
  reader interface gains `MissingOperations(ctx, string, []string)`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(netready): site readiness for the ready network`.

### Task 5: Roles in the site setup run

**Files:**
- Modify: `internal/settings/setup_site.go` (new step `networking-rights` after `serviceAccount`), `internal/settings/bootstrap_prism.go`, `internal/nutanix/iam.go` (`GetRole`, `CreateRole`, `UpdateRoleOperations`, `CreateAuthorizationPolicy`, `DeleteAuthorizationPolicy`, `OperationExtIDs`), `internal/settings/settingstest/fakes.go`, `internal/tenant/provision.go` (`roleMembershipStep`), tests

**Interfaces:**
- Produces:
  - `settings.NetworkingOperations = []string{"View_Subnet","Create_Subnet","Delete_Subnet","Share_Subnet","Unshare_Subnet","View_VPC","View_Floating_Ip","Create_Floating_Ip","Update_Floating_Ip","Delete_Floating_Ip","View_VPC_Route_Table","View_Route_Table_Route","Create_Route_Table_Route","Delete_Route_Table_Route"}`
  - Role `Portico Networking` with those operations; policy `Portico Networking_portico` binding it to the service account with entity filters `subnet *`, `floating_ip *`, `vpc *`, `vpc_route_table *` (spec 4.5 and 6.1: the narrower filter is not measured; my call until it is, recorded in the release notes' known limits).
  - Role `Portico Tenant`: the operations of `Project Admin` minus `Create_Floating_Ip` and `Delete_Floating_Ip`, re-synced (`UpdateRoleOperations`) when it differs.
  - `setupOperations` gains `Create_Role`, `Update_Role`, `Create_Authorization_Policy`, `Delete_Authorization_Policy`.
  - Tenant service: `Config.RoleName` `Portico Tenant`; when the site has no such role and `RoleName` is the default, the role step uses `Project Admin` and its detail says "fallback: Project Admin, Repair the site" (Review Focus 4). `roleMembershipStep` deletes the group's memberships in the project with another role after the right one exists (the move of spec 4.3 at Retry or Repair).

- [ ] **Step 1: Write the failing tests**: `TestSetupCreatesNetworkingRole`
  (fake records a role with the 14 operations and a policy naming the
  service account); `TestSetupResyncsRoleOperations` (an existing role
  with 6 operations is updated, not duplicated);
  `TestSetupCreatesPorticoTenantRole` (Project Admin's operations minus
  the two); `TestSetupStepOrder` (`networking-rights` right after
  `service-account`, failed when the admin lacks `Create_Role`);
  `TestRoleFallbackOnUnrepairedSite` (tenant fake without `Portico
  Tenant` → membership with `role-pa`, step detail contains "Repair the
  site"); `TestRoleMembershipMoves` (a membership with `role-pa` and a
  `Portico Tenant` role present → new membership created, old deleted).
- [ ] **Step 2: Run** `go test ./internal/settings ./internal/tenant ./internal/nutanix` — Expected: FAIL.
- [ ] **Step 3: Implement.** The step reports `ok`, `failed` with the
  missing admin operation, or `kept` when both roles and the policy are
  already right.
- [ ] **Step 4: Run** — Expected: PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(setup): Portico Networking and Portico Tenant roles`.

### Task 6: Building the ready network at Connect

**Files:**
- Create: `internal/tenant/network.go`, `internal/tenant/network_test.go`
- Modify: `internal/tenant/service.go` (`Site` gains `Network settings.SiteNetwork`, `RoutedPrefix netip.Prefix`, `DNS []netip.Addr`; `PrismCentral` gains the Task 1 calls), `internal/tenant/provision.go` (call after `visibility`), `internal/tenant/tenanttest/fakes.go` (in-memory subnets, VPCs, routes, floating IPs; `DropProjectOnce` to reproduce Review Focus 1), `cmd/portico` wiring of `RoutedPrefix` from the transit VPC

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces:
  - `(s *Service) buildNetwork(ctx, t *store.Tenant, p *store.Placement, site Site) error`: sets `NetState` `building`, runs the recorded steps `net-external-subnet`, `net-share`, `net-vpc`, `net-default-route`, `net-subnet`, `net-floating-ips`, `net-read-back`, then `ready`; a failure sets `failed` and `NetError`, never `PlacementFailed`.
  - The `/28`: `p.TransitPrefix` if set, else `netplan.NextFree(site.RoutedPrefix, 28, <TransitPrefix of every placement of the site>)`, stored before the first Prism Central call.
  - `(s *Service) AssignSubnets(ctx) (int, error)`: gives every tenant without a subnet the next free `/24` in creation order; `Create` assigns one at creation (or stores the exception).
  - Each create checks the returned `ProjectExtID` where one was requested; when empty it deletes the object and creates it once more; empty again fails the step "Prism Central created <name> without its project".

- [ ] **Step 1: Write the failing tests**: `TestConnectBuildsNetwork`
  (site with `Enabled`: steps recorded in the order above, `NetState`
  `ready`, objects named `portico-acme-ext|vpc|net|fip-1`, `TransitPrefix`
  `10.23.211.0/28`, one floating IP stored with its address);
  `TestConnectWithoutNetwork` (`Enabled` false → no `net-` step, `NetState`
  `none`); `TestNetworkFailureKeepsConnectionActive` (`CreateVPC` fails →
  `PlacementActive`, `NetState` `failed`, `NetError` the error);
  `TestTwoTenantsTwoSlices` (`10.23.211.0/28` and `.16/28`);
  `TestFailedHalfwayKeepsSlice` (first tenant fails at `net-vpc`; second
  gets `.16/28`); `TestSubnetWithoutProjectRecreated` (`DropProjectOnce`
  on the internal subnet → one delete, second create, `ready`);
  `TestSubnetWithoutProjectTwiceFails` (→ `failed`, message above);
  `TestAssignSubnetsCreationOrder` (three tenants, one with an exception
  `10.100.0.0/24` → `10.100.1.0/24`, `10.100.2.0/24`);
  `TestBuildIsIdempotent` (a second run with everything present creates
  nothing).
- [ ] **Step 2: Run** `go test ./internal/tenant -run 'Network|Slice|Subnet'` — Expected: FAIL.
- [ ] **Step 3: Implement.** Steps find an object by its stored `extId`
  first, by name second, so a retry adopts what a cut-off run created.
- [ ] **Step 4: Run** — Expected: PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(tenant): build the ready network after the connection`.

### Task 7: Disconnect removes the ready network

**Files:**
- Modify: `internal/tenant/provision.go` (`removePlacement`), `internal/tenant/service.go` (`Contents`), `internal/tenant/network.go`, tests

**Interfaces:**
- Consumes: Task 6 stored ids.
- Produces: steps `net-floating-ips`, `net-subnet`, `net-vpc`, `net-unshare`, `net-external-subnet` run before `role-membership`; each tolerates "not found". `Contents` drops from its counts the VPC, subnet and floating IPs whose `extId` the placement stores.

- [ ] **Step 1: Write the failing tests**: `TestDisconnectRemovesNetwork`
  (order above, then the 0.1.0 steps, placement deleted);
  `TestDisconnectRefusesAssociatedFloatingIP` (associated with a NIC →
  `NotEmptyError` naming the floating IP, nothing deleted);
  `TestContentsIgnoresPorticoObjects` (project holds Portico's VPC,
  subnet, one floating IP and one VM → only `1 VMs`);
  `TestContentsCountsTenantFloatingIP` (a floating IP not in the
  placement counts); `TestDisconnectWithAnotherTenant` (the other
  placement's objects untouched); `TestDisconnectAfterTenantDeleted`
  (objects already gone → `already absent` lines, success).
- [ ] **Step 2: Run** `go test ./internal/tenant -run Disconnect\|Contents` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — Expected: PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(tenant): Disconnect removes the ready network`.

### Task 8: Retry network, Create ready network, allowance

**Files:**
- Modify: `internal/tenant/service.go` (`RetryNetwork`, `CreateNetwork`, `SetAllowance`), `internal/api` routes for the same, tests

**Interfaces:**
- Produces: `RetryNetwork(ctx, id, siteID) error` (runs the `net-` steps only, `NetState` must be `failed`); `CreateNetwork(ctx, id, siteID) error` (`none` only, the site's network enabled); `SetAllowance(ctx, id string, n int) (kept int, err error)` (0 to 12; creates or deletes unassociated floating IPs on every ready placement; `kept` counts associated ones it could not delete).

- [ ] **Step 1: Write the failing tests**: `TestRetryNetworkOnlyNetSteps`;
  `TestCreateNetworkForUpgradedConnection`; `TestCreateNetworkRefusedWhenDisabled`;
  `TestAllowanceUp` (1 → 3: two created, `fip-2`, `fip-3`);
  `TestAllowanceDownKeepsAssociated` (3 → 0 with one associated: two
  deleted, `kept` 1); `TestAllowanceBounds` (13 refused).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**, serialised with the tenant's existing lock.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(tenant): retry, create and resize a ready network`.

### Task 9: Admin UI

**Files:**
- Modify: `internal/web/sites.go`, `internal/web/templates/site.html` (Networking card between Connection and Tenants), `internal/web/tenants.go`, `internal/web/templates/tenant.html`, `tenant_new.html`, `settings.html` ("Tenant networks"), `internal/api/sites.go` (`UpdateSite` for `Network`), tests in `internal/web`

**Interfaces:**
- Consumes: Task 4 `Evaluate`, `TransitChoices`, `Ready`; Task 8 calls.
- Produces: `GET /sites/{id}/networking` (card fragment), `POST /sites/{id}/networking` (transit pick, DNS, Integrated confirmation, switch); the switch is refused unless `Ready`. Tenant page: per connection the network state, `/24`, `/28`, SNAT address, floating IPs, and **Retry network** / **Create ready network**; allowance field. Creation form: "Advanced" with subnet exception and allowance. Settings: pool, read-only once any tenant holds a subnet.

- [ ] **Step 1: Write the failing tests**: `TestNetworkingCardChecks`
  (fake reader with a failed gateway → the row and its message);
  `TestNetworkingSwitchRefusedWhenNotReady` (422, message);
  `TestNetworkingSaveTransit`; `TestTenantPageShowsNetwork`
  (`10.23.211.18` and "ready"); `TestRetryNetworkButton` (only on
  `failed`); `TestPoolReadOnlyOnceAssigned`; `TestTenantNewException`
  (an overlapping exception is refused with the other tenant named).
- [ ] **Step 2: Run** `go test ./internal/web` — Expected: FAIL.
- [ ] **Step 3: Implement** following the existing cards (status card
  fragment and its HTMX swap), wording in the spec.
- [ ] **Step 4: Run** — Expected: PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(web): Networking card and the tenant's ready network`.

### Task 10: The site monitor reads the stored objects

**Files:**
- Modify: `internal/sites/monitor.go` or a new `internal/tenant/netwatch.go` (whichever already runs per site; the builder picks), tests

**Interfaces:**
- Produces: on each pass, for every `ready` placement, a `GetVPC`, `GetSubnet` and `GetFloatingIP` by stored id; a missing one sets `failed` "deleted in Prism Central", a changed project or external subnet "changed in Prism Central". Nothing is recreated by the monitor.

- [ ] **Step 1: Write the failing tests**: `TestWatchDeleted`, `TestWatchChanged`, `TestWatchLeavesHealthyAlone`, `TestWatchSiteUnreachableChangesNothing`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(tenant): notice ready networks changed in Prism Central`.

### Task 11: Documents, lab acceptance and release 0.2.0

**Files:**
- Modify: `docs/install.md` (prerequisites of write-up 34 and the transit build: no-NAT VLAN external subnet, transit VPC with one fixed address, default route, routed prefix, the router's route), `docs/superpowers/specs/2026-10-17-portico-design.md` (4.4, 4.5, 2), the sites usability design 4.6, `docs/backlog.md`, `README.md`
- Create: `docs/releases/v0.2.0.md`, `lab/portico/ready-network.sh` (on the appliance: Connect a tenant, check its network, `lab/network/probe-vm.sh` against it, Disconnect), `docs/lab/2026-10-3x-35-ready-network-acceptance.md`

- [ ] **Step 1:** Deploy a dev image on `.191` (`lab/appliance/deploy.sh`), Repair `site-1`, pick `portico-transit` on its Networking card, switch it on.
- [ ] **Step 2: Run** `lab/portico/ready-network.sh` — Expected: the tenant's network `ready`, the probe VM reached on its floating IP and reaching the Internet through the MSP's route, Disconnect leaving only the transit objects.
- [ ] **Step 3:** Write write-up 35 with the measured times and the known limits (the policy's `*` filters until spec 6.1 is measured; one transit VPC per site; Integrated mode confirmed by the MSP).
- [ ] **Step 4:** Release notes and the documents above; `go test ./...`, `lab/test_scripts.sh`.
- [ ] **Step 5: Commit** `docs: 0.2.0`, open the PR. Marco publishes `v0.2.0-rc.1`, then `v0.2.0`.
