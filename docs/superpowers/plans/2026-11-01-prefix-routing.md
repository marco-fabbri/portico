# Prefix Routing (0.3.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A site's Networking card gets a Routing section: Portico builds
the BGP peering of the transit VPC in Prism Central (local gateway, remote
gateway, session) towards the MSP's router or towards an edge router VM it
builds and owns, shows the peering's state, and lets the MSP move between
the two peers and replace the edge; the ready-network switch waits for
the session UP.

**Architecture:** A gateway/session/VM/image part of the Prism Central
client (`internal/nutanix/gateways.go`, `vms.go`, `images.go`), a new
`internal/peering` package that owns the state machine (build, replace,
move, tear down) over the `tenant`-style recorded steps, peering fields on
`settings.SiteNetwork` (a JSON document, no SQL migration), four more
`netready` checks, the card's Routing section in `internal/web`, and a
second image artefact `portico-edge-<version>.qcow2` from the image
workflow. The lab measurements of the design's section 6 come first and
may change Tasks 5 and 6.

**Tech Stack:** Go 1.27 standard library, the existing fakes
(`internal/nutanix`'s `fakePC`, `internal/settings/settingstest`), HTMX
templates, Packer + QEMU, cloud-init, FRR 8.5, nftables. Lab: NTNX-01
(`10.23.210.175`), the appliance `portico-acceptance` (`.193`), the router
`portico-edge` (`.194`), the gateway `portico-transit-bgp` (`.195`).

**Spec:** `docs/superpowers/specs/2026-11-01-prefix-routing-design.md`;
measurements `docs/lab/2026-11-01-37-bgp-gateway.md` and the write-up 38
Task 1 produces.

## Global Constraints

- All text on disk in English; Italian only in chat with Marco.
- Commits: `git -c user.email=9093189+marco-fabbri@users.noreply.github.com commit`,
  message ending with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
  PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
  Merge only with Marco's explicit OK: `gh pr merge N --rebase --delete-branch`;
  one branch and one PR per topic.
- `go test ./...`, `go vet ./...`, `gofmt -l cmd internal` clean, and
  `lab/test_scripts.sh` PASS before every commit that touches them;
  `shellcheck -x` on every lab script.
- Never log, print or return a password, API key, token or cookie value.
  Portico holds no credential, key or password for the edge router.
- Networking API `/api/networking/v4.4/config`: `gateways`,
  `bgp-sessions`, `bgp-sessions/{extId}/bgp-routes`; VM API
  `/api/vmm/v4.0/ahv/config/vms`; images `/api/vmm/v4.0/content/images`;
  every create, update, delete is a task waited with `WaitTask`; updates
  and deletes carry the ETag in `If-Match`.
- Names: `portico-<site>-bgp` (local gateway), `portico-<site>-peer`
  (remote gateway), `portico-<site>-peering` (session),
  `portico-<site>-edge` (edge VM), `portico-edge-<version>` (edge image),
  `portico-<site>-vlan` (the unmanaged VLAN subnet when needed); all in
  the system project `00000000-0000-0000-0000-000000000000`; the edge VM
  carries the category `portico:edge`.
- ASNs: gateway 65010 and edge 65020 proposed, the router's ASN is the
  MSP's; a session whose two ASNs are equal is refused by the card.
- The peering lives while the switch is on or any tenant holds a ready
  network on the site; it is torn down only when both are gone.
- Replace of the edge is break-before-make with its one address; a move
  to the router keeps the edge powered off until the MSP deletes it.
- No NAT, no firewall and no login on the edge router; Portico never
  changes the transit VPC, `MGMT` or a tenant's objects.
- Public repository: no site, customer or company names in product text.

## Review Focus

1. A site upgraded from 0.2.x with the switch on and no peering keeps
   working and shows the `static` peer, never a red card and never a
   Repair that creates a gateway without addresses: Task 4 and Task 7
   assert the default and the check.
2. Save of the Routing section while the local gateway's VM still boots
   (`DOWN`, "Service nginx is down") must wait for `UP` before the session
   and must not record a failure: Task 5 asserts the wait and the bound.
3. A move to the router while tenants hold floating IPs must not tear the
   peering down at any point: the session is updated or recreated, never
   left absent with the switch on; Task 5 asserts no delete before the
   new session exists in the recreate path.
4. The edge VM found by name but without the `portico:edge` category is
   never adopted nor deleted: Task 6 asserts it is reported, not touched.
5. The edge's cloud-init with an address inside the external subnet's pool
   or equal to the gateway's address is refused before anything is
   created: Task 8 asserts the form's refusal.

---

### Task 1: Measure what the design leaves open (section 6)

**Files:**
- Modify: `lab/network/bgp-gateway.sh` (new verbs `update-session`,
  `update-remote`, `subnet-gateway`, `image-url`, `edge-on-external`)
- Create: `docs/lab/2026-11-02-38-peering-updates.md`, a row in
  `docs/lab/README.md`, rows in `docs/lab/lab-inventory.md`
- Modify: `docs/superpowers/specs/2026-11-01-prefix-routing-design.md`
  where a measurement changes a decision (4.3 if item 3 is refused)

**Interfaces:**
- Produces: the facts Tasks 2, 3, 5, 6 and 9 encode: whether `PUT
  /bgp-sessions/{extId}` accepts a new `localGatewayReference` /
  `remoteGatewayReference`; whether `PUT /gateways/{extId}` accepts a new
  remote address; whether `PUT /subnets/{extId}` changes an external
  subnet's gateway while the transit VPC uses it; the body and rights of
  `POST /api/vmm/v4.0/content/images` with a URL source; the operations
  the service account needs; whether a VM with a static address outside
  the pool sits on the external subnet itself; the gateway's time to `UP`.

- [ ] **Step 1: Add the verbs to `lab/network/bgp-gateway.sh`**

`update-session LOCAL_EXTID REMOTE_EXTID`, `update-remote ADDR`,
`subnet-gateway SUBNET_NAME ADDR`, `image-url NAME URL`,
`edge-on-external NAME ADDR` (a throwaway Rocky VM from
`rocky-9-genericcloud` with a NIC on `external-routed` and cloud-init
setting ADDR/24, deleted by `delete-vm NAME`). Each prints the task
status or the refusal body. `shellcheck -x` clean.

- [ ] **Step 2: Run the measurements on NTNX-01 with the service account of `.193`**

Run, with `source lab/env.sh`: item 1 by creating a second remote gateway
`portico-edge-2` (address `.196`, ASN 65021) and `update-session`; item 2
by `update-remote 10.23.210.196` on `portico-edge`; item 3 by
`subnet-gateway external-routed 10.23.210.254` and back to `.194`; item 4
by `image-url portico-edge-test https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2`
(the release's URL once Task 10 exists); item 9 by `edge-on-external`;
item 5 by reading the IAM operations catalogue (a temporary key for the
service account was planned and not created); item 6 by powering `portico-edge` off for ten minutes and
reading `bgp-sessions` every minute. Expected: a status per item, and the
FRR summary on `portico-edge` before and after each change.

- [ ] **Step 3: Write write-up 38 and update the design**

Sections as `docs/lab/TEMPLATE.md`; one MEASURED line per item with the
request shape that worked and the exact refusal text that did not. If
item 3 is refused, 4.3 of the design says the edge keeps its address for
life and a change of address is a tear-down and rebuild of the peering;
if item 9 is refused, section 3 keeps only the unmanaged VLAN subnet.

- [ ] **Step 4: Restore the lab**

`portico-edge-2` deleted, `portico-edge` back at `.194`, `external-routed`
gateway `.194`, the test VM and image deleted, the FRR summary
`Established`. Run `lab/network/bgp-gateway.sh show`.
Expected: one session UP, two gateways, the routed prefix in the routes.

- [ ] **Step 5: Commit and open the PR**

```bash
git add lab/network/bgp-gateway.sh docs/lab docs/superpowers/specs/2026-11-01-prefix-routing-design.md
git commit -m "lab(network): the peering updates measured (write-up 38)"
```

### Task 2: Gateways and sessions in the Prism Central client

**Files:**
- Create: `internal/nutanix/gateways.go`, `internal/nutanix/gateways_test.go`
- Modify: `internal/nutanix/fakepc_test.go` (routes for `gateways`,
  `bgp-sessions`, `bgp-routes`)

**Interfaces:**
- Consumes: `createWithTask`, `inProject`, `do`, `etagOf`, `WaitTask`
  (`internal/nutanix/networking.go`, `client.go`).
- Produces:
  ```go
  type Gateway struct { ExtID, Name, ProjectExtID, VMExtID, Version string; Local bool; Address netip.Addr; ASN int; State, Message string }
  type BGPSession struct { ExtID, Name, LocalExtID, RemoteExtID string; State, Message string; AdvertiseAll bool }
  type BGPRoute struct { Prefix netip.Prefix; NextHop netip.Addr }
  func (c *Client) ListGateways(ctx) ([]Gateway, error)
  func (c *Client) GetGateway(ctx, extID string) (*Gateway, error)
  func (c *Client) CreateLocalBGPGateway(ctx, name, projectExtID, clusterExtID, subnetExtID string, addr netip.Prefix, gw netip.Addr, asn int, vpcExtID string) (*Gateway, error)
  func (c *Client) CreateRemoteBGPGateway(ctx, name, projectExtID string, addr netip.Addr, asn int) (*Gateway, error)
  func (c *Client) UpdateRemoteGateway(ctx, extID string, addr netip.Addr, asn int) error
  func (c *Client) DeleteGateway(ctx, extID string) error
  func (c *Client) ListBGPSessions(ctx) ([]BGPSession, error)
  func (c *Client) GetBGPSession(ctx, extID string) (*BGPSession, error)
  func (c *Client) CreateBGPSession(ctx, name, projectExtID, localExtID, remoteExtID string, localAddr netip.Addr) (*BGPSession, error)
  func (c *Client) UpdateBGPSession(ctx, extID, localExtID, remoteExtID string) error
  func (c *Client) DeleteBGPSession(ctx, extID string) error
  func (c *Client) BGPRoutes(ctx, sessionExtID string) ([]BGPRoute, error)
  ```
  `State` is `status.state` (`UP`, `DOWN`, `""`), `Message` its message.
  Bodies as `lab/network/bgp-gateway.sh` sends them (write-up 37), with
  `projectExtId` always set.

- [ ] **Step 1: Write the failing tests**

`TestCreateLocalBGPGatewayBodyAndReadBack` (the POST body holds
`projectExtId`, `deployment.interfaces[0].ipAddress.ipv4.value`,
`services.$objectType` `networking.v4.config.LocalNetworkServices`,
`localBgpService.asn` 65010 and `vpcReference`; the read-back picks the
gateway by name in the system project, as `inProject`),
`TestCreateRemoteBGPGatewayBody` (`RemoteNetworkServices`,
`remoteBgpService.address.ipv4.value`, `asn`),
`TestCreateBGPSessionBody` (`shouldAdvertiseAllExternallyRoutablePrefixes`
true, `localGatewayInterfaceIpAddress`), `TestUpdateBGPSessionSendsETag`
(`If-Match` from the GET), `TestBGPRoutesParsesPrefixAndNextHop`
(`100.96.0.0/24` via `10.23.210.200`), `TestGatewayStateFromStatus`
(`DOWN` with "Service nginx is down"; absent status gives `""`).

- [ ] **Step 2: Run them**

Run: `go test ./internal/nutanix/ -run 'Gateway|BGP' -v`
Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement `gateways.go` and the fake's routes**

The raw types mirror the API (`status.state`, `status.message`,
`installedSoftwareVersion`, `vmReference`, `services.localBgpService`,
`services.remoteBgpService.address.ipv4.value`); `UpdateRemoteGateway`
and `UpdateBGPSession` do GET, change the fields, PUT with the ETag, wait
the task, as `UpdateFloatingIP` would.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/nutanix/ -v`
Expected: PASS, all.

- [ ] **Step 5: Commit**

```bash
git add internal/nutanix/gateways.go internal/nutanix/gateways_test.go internal/nutanix/fakepc_test.go
git commit -m "nutanix: BGP gateways, sessions and routes"
```

### Task 3: VMs, images and categories in the Prism Central client

**Files:**
- Create: `internal/nutanix/vms.go`, `internal/nutanix/vms_test.go`,
  `internal/nutanix/images.go`, `internal/nutanix/images_test.go`
- Modify: `internal/nutanix/networking.go` (`UpdateSubnetGateway`),
  `internal/nutanix/fakepc_test.go`

**Interfaces:**
- Produces:
  ```go
  type VM struct { ExtID, Name, ClusterExtID string; PowerOn bool; Categories []string }
  type VMSpec struct { Name, ClusterExtID, ImageExtID, SubnetExtID string; VCPUs int; MemoryBytes, DiskBytes int64; UserData string; CategoryExtID string }
  func (c *Client) ListVMs(ctx) ([]VM, error)
  func (c *Client) GetVM(ctx, extID string) (*VM, error)
  func (c *Client) CreateVM(ctx, s VMSpec) (*VM, error)       // cloud-init CONFIG_DRIVE_V2, legacy BIOS, as lab/network/edge-router.sh
  func (c *Client) PowerVM(ctx, extID string, on bool) error  // $actions/power-on, power-off (hard, the edge has no agent)
  func (c *Client) DeleteVM(ctx, extID string) error
  func (c *Client) EnsureCategory(ctx, key, value string) (extID string, err error) // /api/prism/v4.0/config/categories
  type Image struct { ExtID, Name string; SizeBytes int64 }
  func (c *Client) ListImages(ctx) ([]Image, error)
  func (c *Client) FindImage(ctx, name string) (*Image, error)
  func (c *Client) CreateImageFromURL(ctx, name, url string) (*Image, error) // vmm.v4.content.UrlSource, as Task 1 item 4 measured
  func (c *Client) DeleteImage(ctx, extID string) error
  func (c *Client) UpdateSubnetGateway(ctx, extID string, gw netip.Addr) error // PUT with ETag, as Task 1 item 3 measured
  ```
  `VM.Categories` holds `key:value` strings read from the VM's
  `categories` references resolved once through `ListCategories`.

- [ ] **Step 1: Write the failing tests**

`TestCreateVMBodyHasCloudInitAndCategory`, `TestPowerVMOffIsAnAction`,
`TestEnsureCategoryCreatesOnce` (second call finds it),
`TestCreateImageFromURLSource`, `TestUpdateSubnetGatewayKeepsOtherFields`
(the PUT body is the GET body with the gateway changed, `If-Match` set).

- [ ] **Step 2: Run them**

Run: `go test ./internal/nutanix/ -run 'VM|Category|Image|SubnetGateway' -v`
Expected: FAIL, undefined.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the package's tests**

Run: `go test ./internal/nutanix/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/nutanix
git commit -m "nutanix: VMs with cloud-init, images from a URL, categories, the external subnet's gateway"
```

### Task 4: The peering on the site's settings

**Files:**
- Modify: `internal/settings/sites.go` (`SitePeering` on `SiteNetwork`),
  `internal/settings/sites_test.go`
- Modify: `internal/tenant/service.go` (`Site.Network` carries the
  peering state the switch guard reads)

**Interfaces:**
- Produces:
  ```go
  const ( PeerStatic = "static"; PeerRouter = "router"; PeerEdge = "edge" )
  const ( PeeringNone = "none"; PeeringBuilding = "building"; PeeringUp = "up"; PeeringDown = "down"; PeeringFailed = "failed" )
  type SitePeering struct {
      Kind string `json:"kind,omitempty"`                 // static, router, edge; "" reads as static
      GatewayAddr netip.Prefix `json:"gateway_addr,omitempty"`; GatewayGW netip.Addr `json:"gateway_gw,omitempty"`; GatewayASN int `json:"gateway_asn,omitempty"`
      RouterAddr netip.Addr `json:"router_addr,omitempty"`; RouterASN int `json:"router_asn,omitempty"`
      EdgeAddr netip.Addr `json:"edge_addr,omitempty"`; EdgeASN int `json:"edge_asn,omitempty"`; EdgeKept bool `json:"edge_kept,omitempty"`
      RouteAdded bool `json:"route_added,omitempty"`
      LocalGatewayExtID, RemoteGatewayExtID, SessionExtID, EdgeVMExtID, EdgeImageExtID, VLANSubnetExtID string
      State string `json:"state,omitempty"`; Error string `json:"error,omitempty"`; StateAt time.Time `json:"state_at,omitempty"`
  }
  func (p SitePeering) KindOrStatic() string
  func (p SitePeering) Validate(external netip.Prefix, pool [2]netip.Addr) error // addresses on the VLAN, outside the pool, distinct; ASNs distinct and private (64512-65534, 4200000000-4294967294)
  func (n SiteNetwork) SwitchAllowed() (bool, string)  // false with the reason when Kind is router/edge and State != up
  ```
  `SiteNetwork.Peering SitePeering \`json:"peering,omitempty"\``.

- [ ] **Step 1: Write the failing tests**

`TestPeeringKindDefaultsToStatic` (a 0.2.2 settings document decodes
with `KindOrStatic() == "static"` and `SwitchAllowed()` true),
`TestPeeringValidateRefusesPoolAndEqualASN`, `TestSwitchWaitsForSessionUp`
(`router` with `State` `building` gives false, "the BGP session is not
UP yet"), `TestSettingsRoundTripKeepsPeering`.

- [ ] **Step 2: Run them**

Run: `go test ./internal/settings/ -run Peering -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/settings/ ./internal/tenant/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/settings internal/tenant/service.go
git commit -m "settings: the site's peering, static for every site built before"
```

### Task 5: The peering service

**Files:**
- Create: `internal/peering/peering.go`, `internal/peering/steps.go`,
  `internal/peering/peering_test.go`, `internal/peering/fake_test.go`

**Interfaces:**
- Consumes: Task 2 and Task 3 methods behind an interface
  `PrismCentral` declared here (gateways, sessions, routes, VMs, images,
  categories, `GetSubnet`, `UpdateSubnetGateway`, `ListClusters`,
  `CreateVLANSubnet(ctx, name, clusterExtID string, vlanID int, virtualSwitchExtID string)`, needed: write-up 38
  refuses a VM NIC on the external subnet); `settings.SitePeering`.
- Produces:
  ```go
  type Service struct{ ... } // New(pc PrismCentral, log *slog.Logger, save func(ctx, settings.SitePeering) error)
  func (s *Service) Build(ctx, site settings.Site, p settings.SitePeering, edge EdgeBuilder) (settings.SitePeering, error)   // gateway-local (wait UP, 10 min bound, 20 s poll), gateway-remote, edge (kind edge), session, read-back
  func (s *Service) Refresh(ctx, p settings.SitePeering) (settings.SitePeering, error)                                   // State from Prism Central: up/down with the message
  func (s *Service) ReplaceEdge(ctx, site, p, edge EdgeBuilder) (settings.SitePeering, error)                            // power off, delete, create, wait session UP
  func (s *Service) MoveToRouter(ctx, site, p) (settings.SitePeering, error)                                              // PUT the remote gateway (address, ASN; write-up 38), wait UP, subnet gateway back, edge off, EdgeKept
  func (s *Service) MoveToEdge(ctx, site, p, edge EdgeBuilder) (settings.SitePeering, error)
  func (s *Service) BackToEdge(ctx, site, p) (settings.SitePeering, error)
  func (s *Service) DeleteEdge(ctx, p) (settings.SitePeering, error)
  func (s *Service) TearDown(ctx, site, p) (settings.SitePeering, error)                                                  // session, gateways, edge VM, subnet gateway back
  type EdgeBuilder interface { Build(ctx, site settings.Site, p settings.SitePeering) (vmExtID string, err error) }      // Task 6
  ```
  Steps are recorded as the tenant service's (`name`, outcome) and the
  first failure sets `State` `failed` with `Error` `"<step>: <reason>"`.

- [ ] **Step 1: Write the failing tests**

`TestBuildRouterCreatesThreeObjectsAndWaitsForUp` (the fake answers
`DOWN` twice then `UP`; the session create comes after; `State` `up`),
`TestBuildStopsAtGatewayNotUpWithinBound` (bound reached: `failed`,
`Error` names `gateway-local`), `TestBuildAdoptsExistingByNameInSystemProject`
(a gateway `portico-s1-bgp` in the system project is reused; one in a
tenant's project is refused with "belongs to another project"),
`TestMoveToRouterUpdatesRemoteGateway` (one PUT with the router's
address and ASN, no session delete), `TestBuildRecreatesSessionWhenItsReferencesDiffer`
(a session by name pointing at other gateways is deleted and created
again, the delete after the gateways exist), `TestMoveToRouterKeepsEdgeOff`
(`PowerVM(off)`, no `DeleteVM`, `EdgeKept` true), `TestReplaceEdgeIsBreakBeforeMake`
(power off and delete precede the create), `TestTearDownOrder` (session,
gateways, VM, subnet gateway), `TestRefreshReadsDownMessage`.

- [ ] **Step 2: Run them**

Run: `go test ./internal/peering/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

The wait for `UP` polls `GetGateway` every 20 s for at most 10 min
(both as package variables the tests shorten).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/peering/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/peering
git commit -m "peering: build, refresh, move, replace and tear down the transit VPC's BGP peering"
```

### Task 6: The edge router: cloud-init, image, VM

**Files:**
- Create: `internal/peering/edge.go`, `internal/peering/edge_test.go`,
  `internal/peering/edge_userdata.tmpl`

**Interfaces:**
- Consumes: Task 3 (`CreateVM`, `EnsureCategory`, `FindImage`,
  `CreateImageFromURL`, `GetVM`, `ListVMs`), the release version
  (`main.version`, set by `-ldflags -X main.version=$VER` in the image
  workflow; `cmd/portico` passes it to the web handler and to `Edge`).
- Produces:
  ```go
  type Edge struct { ImageURL string; Version string } // implements EdgeBuilder
  func EdgeUserData(name string, addr netip.Prefix, gw netip.Addr, dns []netip.Addr, asn int, peer netip.Addr, peerASN int) (string, error)
  func (e Edge) Build(ctx, site settings.Site, p settings.SitePeering) (string, error)
  var ErrEdgeNotOurs = errors.New("peering: a VM with the edge's name exists without the portico:edge category")
  ```
  `Build`: image `portico-edge-<version>` found by name or imported from
  `ImageURL`; the category `portico:edge` ensured; a VM by name is
  adopted only with that category, else `ErrEdgeNotOurs`; the VM is
  1 vCPU, 1 GiB, the image's disk, one NIC on the external subnet (or the
  VLAN subnet of Task 1 item 9), user data from `EdgeUserData`.

- [ ] **Step 1: Write the failing tests**

`TestEdgeUserDataHasNoUserNoKeyNoNAT` (the cloud-config has no `users`,
no `ssh_authorized_keys`, no `masquerade`; `ssh_pwauth: false`; FRR block
with `router bgp 65020`, `neighbor 10.23.210.195 remote-as 65010`,
`no bgp ebgp-requires-policy`; nftables input accepts tcp 179 from the
peer only; `net.ipv4.ip_forward = 1`; `route` absent),
`TestEdgeBuildImportsImageOnce`, `TestEdgeBuildRefusesForeignVM`
(`ErrEdgeNotOurs`, no delete), `TestEdgeBuildReusesOurVM`.

- [ ] **Step 2: Run them**

Run: `go test ./internal/peering/ -run Edge -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/peering/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/peering
git commit -m "peering: the edge router, built from the release's image with cloud-init and no login"
```

### Task 7: The card's four checks and the switch guard

**Files:**
- Modify: `internal/netready/netready.go` (`Reader` gains `GetGateway`,
  `GetBGPSession`, `BGPRoutes`, `GetVM`; `Evaluate` gains the peering),
  `internal/netready/netready_test.go`
- Modify: `internal/web/networking.go` (`Refused` reads
  `SwitchAllowed`)

**Interfaces:**
- Consumes: Task 2, 3, 4.
- Produces: checks named `bgp gateway`, `bgp session`, `advertised`,
  `edge router` (edge only), and the warning `route on your core` (edge,
  until `RouteAdded`) or `peer on your router` (router, until the session
  is UP) with the exact lines the MSP needs; for `static`: one `ok` row
  "routed by your router (not checked)".

- [ ] **Step 1: Write the failing tests**

`TestEvaluateStaticAddsOneUncheckedRow`, `TestEvaluateRouterSessionDown`
(the message from Prism Central and the three router lines),
`TestEvaluateEdgeAdvertisedPrefixMissing` (routes without the routed
prefix: `failed`, "The routed prefix is not advertised: <what is>"),
`TestEvaluateEdgeRouteNotTicked` (warning with `100.96.0.0/24 via
10.23.210.194`), `TestSwitchRefusedWhilePeeringBuilds` (in `networking.go`'s
test).

- [ ] **Step 2: Run them**

Run: `go test ./internal/netready/ ./internal/web/ -run 'Evaluate|Switch' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/netready/ ./internal/web/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/netready internal/web/networking.go internal/web/networking_test.go
git commit -m "netready: the peering's checks; the switch waits for the session"
```

### Task 8: The Routing section of the Networking card

**Files:**
- Modify: `internal/web/templates/site.html` (`site_networking`: the
  Routing section), `internal/web/networking.go` (routes and handlers),
  `internal/web/networking_test.go`, `internal/web/web.go` (the peering
  service wired with a save closure)

**Interfaces:**
- Consumes: Task 4 (`SitePeering`, `Validate`), Task 5, Task 6.
- Produces routes:
  `POST /setup/sites/{id}/routing` (save: kind, addresses, ASNs; builds
  in the background as the ready network does, the card polls),
  `POST /setup/sites/{id}/routing/{action}` with `action` in
  `route-added`, `replace-edge`, `move-router`, `move-edge`,
  `back-to-edge`, `delete-edge`, `repair`; `GET` of the card shows the
  peering's state, the lines box ("On your router: …" / "On your core:
  …") and the buttons the state allows.

- [ ] **Step 1: Write the failing tests**

`TestRoutingSaveRefusesAddressInPool` (Review Focus 5: 400 with the
reason, nothing built), `TestRoutingSaveRouterBuilds` (the fake peering
service records a Build with kind router), `TestRoutingReplaceEdgeNeedsConfirm`
(the first POST answers the confirm page naming the three minutes; the
confirmed POST calls `ReplaceEdge`), `TestRoutingLinesForRouter` (the
page holds `neighbor 10.23.210.195 remote-as 65010`),
`TestRoutingStaticShowsRecommendation`,
`TestSwitchOffKeepsPeeringWhileNetworksExist` (the switch off with a
tenant holding a ready network on the site records no `TearDown`; with
none it does), and in `internal/tenant` `TestDisconnectLastNetworkTearsDownPeering`
(Disconnect of the last ready network on a site whose switch is off
calls the peering hook `OnLastNetworkGone(ctx, siteID)` the service
exposes as a field).

- [ ] **Step 2: Run them**

Run: `go test ./internal/web/ -run Routing -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Follow `networkAction` for the action handlers and the confirm template
for `replace-edge` and `delete-edge`. The tear-down guard lives in one
function `peeringNeeded(ctx, siteID) bool` (switch on, or any placement
on the site with `NetState` `ready` or `building`) used by the switch
handler and by the tenant service's hook.

- [ ] **Step 4: Run the tests and look at the card**

Run: `go test ./internal/web/` then `go run ./cmd/portico serve` with the
dev settings and open the site's page.
Expected: ok; the Routing section renders in the three kinds.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "web: the Routing section of the Networking card"
```

### Task 9: The service account's rights

**Files:**
- Modify: `internal/settings/networking_ops.go`,
  `internal/settings/networking_rights.go`,
  `internal/settings/networking_rights_test.go`

**Interfaces:**
- Consumes: write-up 38's list: `View_Network_Gateway`, `Create_Network_Gateway`,
  `Update_Network_Gateway`, `Delete_Network_Gateway`, `View_Bgp_Session`,
  `Create_Bgp_Session`, `Update_Bgp_Session`, `Delete_Bgp_Session`,
  `View_Bgp_Route`, `View_Image`, `Create_Image`, `Delete_Image`,
  `View_Virtual_Machine`, `Create_Virtual_Machine`, `Update_Virtual_Machine`,
  `Delete_Virtual_Machine`, `Allow_Virtual_Machine_Power_On`,
  `Allow_Virtual_Machine_Power_Off`, `Update_Virtual_Machine_Categories`,
  `View_Category`, `Create_Category`, `Update_Subnet`, `View_Cluster`.
- Produces: `NetworkingOperations` extended with that list;
  `networkingEntityTypes` extended with `network_gateway`, `bgp_session`,
  `vm`, `image`, `category`, `cluster`; Repair offers the new operations
  to a site set up before (the existing `networkingRights` diff); the
  acceptance (Task 13) is where sufficiency is proven, the temporary-key
  measurement of Task 1 was not run.

- [ ] **Step 1: Write the failing test**

`TestNetworkingRightsRepairAddsPeeringOperations` (a role holding the
0.2.2 list is updated, the diff names the new operations).

- [ ] **Step 2: Run it** — Expected: FAIL.

- [ ] **Step 3: Implement** — [ ] **Step 4: Run** `go test ./internal/settings/` — Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/settings
git commit -m "settings: the networking rights cover the peering and the edge router"
```

### Task 10: The edge image in the image pipeline

**Files:**
- Create: `image/edge.pkr.hcl`, `image/scripts/edge-provision.sh`,
  `image/oscap/edge-tailoring.xml`, `image/scripts/edge-boottest.sh`
- Modify: `image/build.sh` (second artefact), `.github/workflows/image.yml`
  (build, boot test, collect `portico-edge-<version>.qcow2` and its
  sha256), `image/README.md`

**Interfaces:**
- Produces: `portico-edge-<version>.qcow2` on the release, Rocky 9
  GenericCloud base (`iso_url` the GenericCloud qcow2, `disk_image` true),
  FRR and nftables installed, `bgpd=yes`, no user, no password, no SSH
  host key, cloud-init enabled; CIS Level 1 Server with the appliance's
  exceptions plus `sysctl_net_ipv4_ip_forward` selected false (the edge
  forwards) and the SSH rules not applicable (no sshd).

- [ ] **Step 1: Write the boot test**

`image/scripts/edge-boottest.sh IMAGE`: boots the image under QEMU with a
config drive from `EdgeUserData`'s shape (a fixture file), waits for the
serial console to print `portico-edge ready` (the cloud-init `runcmd`'s
last line), asserts `vtysh -c 'show running-config'` through the serial
console holds `router bgp 65020`, and that port 22 does not answer.

- [ ] **Step 2: Run it against the appliance image** — Expected: FAIL (no FRR, sshd answers).

- [ ] **Step 3: Write `edge.pkr.hcl`, the provision script, the tailoring; extend `build.sh` and the workflow**

- [ ] **Step 4: Build and boot-test locally or by the workflow run by hand**

Run: `gh workflow run image.yml` then the boot test job.
Expected: both artefacts attached, the edge boot test green.

- [ ] **Step 5: Commit**

```bash
git add image .github/workflows/image.yml
git commit -m "image: the edge router image, a second artefact of the release"
```

### Task 11: The monitor refreshes the peering

**Files:**
- Modify: `internal/sites/monitor.go` (a peering refresh beside the
  probes, at the monitor's pace), `internal/sites/monitor_test.go`,
  `internal/web/sites.go` (the site's page shows the peering state)

**Interfaces:**
- Consumes: Task 5 `Refresh`.
- Produces: `Monitor.SetPeeringRefresh(f map[string]func(ctx) error)`;
  the site row shows "peering: up/down" when the kind is router or edge.

- [ ] **Step 1: Write the failing test** — `TestMonitorRunsPeeringRefreshAtItsPace`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement** — [ ] **Step 4: Run** `go test ./internal/sites/ ./internal/web/` — Expected: ok.
- [ ] **Step 5: Commit** — `git commit -m "sites: the monitor refreshes the peering's state"`.

### Task 12: Documentation and release notes

**Files:**
- Modify: `docs/install.md` (step 3 as design section 5; the addresses
  the MSP sets aside; VyOS named), `docs/superpowers/specs/2026-10-29-tenant-network-design.md`
  (4.1 pointer), `docs/backlog.md` (HA of the edge; the done item
  removed), `README.md` (status and the 0.3.0 row), `image/README.md`
- Create: `docs/releases/v0.3.0.md`

- [ ] **Step 1: Write the pages** as the design's section 5 lists them;
  the release notes name what changed, what the MSP must give, the two
  artefacts and the known limits (no HA of the edge, break-before-make).
- [ ] **Step 2: Check** — `grep -ri '\blab\b' docs/install.md docs/releases/v0.3.0.md README.md` prints nothing.
- [ ] **Step 3: Commit** — `git commit -m "docs: prefix routing in the install guide, the design, the backlog and the 0.3.0 notes"`.

### Task 13: Acceptance on the lab and the release

**Files:**
- Create: `docs/lab/2026-11-xx-39-prefix-routing-acceptance.md`, rows in
  `docs/lab/README.md` and `docs/lab/lab-inventory.md`
- Modify: `lab/network/bgp-gateway.sh` (`delete` used to clear the lab's
  hand-made peering first)

- [ ] **Step 1: Clear the hand-made peering** — `lab/network/bgp-gateway.sh delete`; `portico-edge` stays (the lab's router).
- [ ] **Step 2: Upgrade `.193` to the branch build** — `bash deploy/install.sh --release <dev build>`; Repair the site's rights.
- [ ] **Step 3: Router mode** — Routing: router `.194` ASN 65020; FRR on `portico-edge` already peers; expect the session UP, the switch allowed, a tenant's VM reached on its floating IP from the build host.
- [ ] **Step 4: Move to edge** — Routing: edge `.196`; expect the edge VM built from the release image, the session UP through it, the static route on `portico-edge` replaced by `100.96.0.0/24 via 10.23.210.196` by hand (the lab's "core"), the VM reached.
- [ ] **Step 5: Replace and move back** — Replace the edge (time the gap with a ping on the floating IP), move to router (edge kept off), back to edge, delete edge.
- [ ] **Step 6: Write write-up 39** with the times measured and anything that differs from the design; update the inventory.
- [ ] **Step 7: Commit, PR, Marco's OK, tag `v0.3.0`** (the tag and the publish are Marco's, as the explicit-authorisation rule says).
