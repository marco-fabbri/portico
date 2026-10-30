# 35 — Ready network acceptance on a second appliance

## Question

Does 0.2.0's ready network work end to end on a freshly installed
appliance (Task 11 of the ready-network plan): the site setup creates the
networking rights, the site's Networking card passes its checks, a tenant
connected to the site gets its network, a VM on it is reached on its
floating IP and reaches the Internet, and Disconnect leaves only the MSP's
transit objects?

## Measured on

- Prism Central: pc.7.6.0.6 on NTNX-01 (`10.23.210.175`, site AZ-IT-MI1),
  AOS 7.6, Flow Virtual Networking in the Integrated mode
- Portico: a second appliance, `portico-acceptance` (`10.23.210.193`,
  public names `portal.acceptance.portico.test` and
  `id.acceptance.portico.test`), dev builds `574527e`, then `f384395`
- Date: 2026-10-30
- By: Claude, with Marco's agreement for the site setup (IAM grants)

## Method

1. Second appliance deployed next to the first with
   `LAB_VM_NAME=portico-acceptance LAB_APPLIANCE_STATE=lab/appliance/b
   lab/appliance/deploy.sh`, its setup through the API.
2. Site added and set up in one call (`POST /api/v1/sites` with `admin`
   and `service_account: portico-acceptance`); repaired after the fix
   below with `POST /api/v1/sites/az-it-mi1/setup`.
3. Networking card saved from the admin UI: the transit VPC
   `portico-transit` (routed prefix `10.23.211.0/24`), DNS `1.1.1.1`,
   Integrated mode confirmed, switched on.
4. `LAB_JUMP=build@10.23.210.192 lab/portico/ready-network.sh az-it-mi1`:
   tenant `netcheck`, its network, `lab/network/probe-vm.sh` against it
   from the build host (the only host with a route to the routed prefix),
   Disconnect, and a check that no subnet, VPC or floating IP of the
   project is left.

## Result

### The site setup

- MEASURED: the first setup stopped at `networking-rights`: Prism Central
  answered 400 "Failed to create role as invalid operations are provided
  in the request" to the *Portico Tenant* role.
- MEASURED: pc.7.6 marks operations with an `operationType`; eight are
  `SYSTEM_DEFINED_ONLY` (`GET /api/iam/v4.1.b3/authz/operations` with
  `$filter=operationType eq Iam.Authz.OperationType'SYSTEM_DEFINED_ONLY'`)
  and a custom role cannot hold them. Project Admin holds two:
  `Create_Role_Membership` and `Delete_Role_Membership`.
- MEASURED: with them left out (fix `f384395`), the Repair completed;
  *Portico Tenant* has 451 operations: Project Admin's 455 less the two
  floating IP operations and the two system-only ones. A tenant therefore
  cannot manage its project's members in Prism Central (known limit of
  the release).
- MEASURED: the Networking card passed its nine checks (Flow, mode,
  gateway, transit picked, transit valid, rights, tenant role, DNS, pool:
  "0 of 16 tenant ranges used") and saved "new connections to this site
  get a ready network".

### The tenant's network

| run | network ready | Disconnect | note |
|---|---|---|---|
| 1 | 64 s | 70 s | VM not reached: probed from the workstation, which has no route to the routed prefix |
| 2 | — | — | connection failed: Prism Central timed out the role lookup; Retry made it |
| 3 | 47 s after Retry | 67 s | the probe script read an empty subnet list (fixed, `7b781ad`) |
| 4 | 100 s | 80 s | VM reached; Internet not reached |
| 5-7 | 47 s; 24 s after Retry | — | routed prefix `100.96.0.0/24` behind a router VM: the probe scripts took failed reads for missing objects (fixed) |
| 8 | ready (run 7's) | 50 s | VM reached on `100.96.0.12`; Internet 200 in 0.21 s; nothing left |

- MEASURED: each run that connected built the seven `net-` steps, the
  last the read-back:
  external subnet `portico-netcheck-ext` `10.23.211.0/28`, shared with
  the project, VPC with its default route, subnet `portico-netcheck-net`
  `10.100.0.0/24`, one floating IP from the `/28`, SNAT address read back.
- MEASURED: the VM got `10.100.0.74/24` by DHCP, default route via
  `10.100.0.1`, nameserver `1.1.1.1`, and was reached over SSH on its
  floating IP `10.23.211.11` from the build host.
- MEASURED: the VM did not reach the Internet (`curl` code 000 after
  10 s). `10.23.211.18` (another floating IP of the routed prefix) answers
  ping from the build host, which routes `10.23.211.0/24 via
  10.23.210.200` itself, and not from `.193`, which goes through the
  router: the router has no route for the routed prefix yet. That route
  and its NAT are the MSP's (install guide, "The transit VPC, once per
  site").
- MEASURED: every Disconnect removed the floating IP, the subnet, the VPC
  and the external subnet; no object named `portico-netcheck-` was left,
  and the transit VPC stayed.

### A router in front of the transit VPC

The routed prefix `10.23.211.0/24` turned out to clash with the address
plan, where the third octet is the VLAN ID, and the site's router could
not be changed. A router VM took its place for the routed prefix:

- `portico-edge` on NTNX-01 (`10.23.210.194`, 1 vCPU, 1 GiB, Rocky 9,
  `lab/network/edge-router.sh create`): IP forwarding on, ICMP redirects
  off, a route `100.96.0.0/24 via 10.23.210.200`, and nftables
  masquerading the routed prefix to its own address, so the site's router
  sees one more host of the VLAN.
- `external-routed` made again with `EXT_GATEWAY=10.23.210.194`; the
  transit VPC made again with `ERP=100.96.0.0/24` and no overlay external
  subnet of its own (`OEN_CIDR=none`); the test project
  `portico-routedprobe` and its objects deleted first. The Networking card
  took the new transit VPC and passed its nine checks ("routed prefix
  100.96.0.0/24").
- MEASURED: the tenant got `100.96.0.0/28`, SNAT `100.96.0.13`, floating
  IP `100.96.0.12`. The VM was reached on it from the build host (route
  `100.96.0.0/24 via 10.23.210.200`, not persistent), and reached the
  Internet: `curl` code 200 in 0.21 to 0.54 s. A capture on the router
  showed the VM's traffic leaving with its floating IP as source and the
  answers coming back through it; the build host's ping did not cross
  the router (the transit VPC reaches hosts of its own VLAN directly).
- MEASURED: Prism Central kept failing reads during these runs (504, 401,
  error text). The probe scripts took a failed list for a missing project,
  floating IP or VM, and the final check took one for "nothing left":
  `probe-vm.sh` now reads up to five times and tells "absent" from "not
  answered", and `ready-network.sh` fails a list that never answers.
  Portico itself recovered each time with Retry network, which adopted
  the external subnet it had made and found the sharing done.

### Prism Central under load

- MEASURED: NTNX-01's Prism Central restarted at about 00:15 UTC; from
  then its Cassandra restarted repeatedly (from 07:05 about every two
  minutes): `cassandra_monitor` kills it when its heartbeat times out,
  and Cassandra's log shows the heartbeat waiting 3 to 8 s for a CPU. The
  Prism Central VM's disks answered in 2 to 15 ms; its load average was
  96 on 8 vCPUs during the restarts. Both nested clusters share
  `proxmox01` (16 and 12 cores on 32 threads at 1.8 GHz).
- MEASURED: during the restarts, API calls answered 504 "upstream
  request timeout", 500 "Failed to store new cookie in IDF", or nothing
  within 30 s. The site setup stopped at a different step each time and
  completed when Prism Central answered again; a connection failed on a
  timeout and Retry built it.

## Implications for the design

- *Portico Tenant* is Project Admin without the floating IP operations
  and without the system-only operations (tenant network design 4.3,
  release notes): tenants do not manage their project's members.
- No change for the slow Prism Central: every setup step and every
  network step is idempotent, so Repair and Retry recover; the error
  says what Prism Central answered.

- None from the router: the routed prefix is the MSP's network, and
  Portico works with any block. A test site whose router cannot be
  changed can put a router VM in front, as here; for an MSP that is a
  single point of failure, and its own router or BGP gateway does it.
- The default tenant pool `10.100.0.0/16` overlaps Prism Central's
  microservices subnets (the Prism Central VM's pod network here is
  `10.100.0.0/24`, the tenant's `/24` in every run): 0.2.1 changes the
  default (ADR 0009).

## How to re-measure after an upgrade

1. `GET /api/iam/v4.1.b3/authz/operations?$filter=operationType eq
   Iam.Authz.OperationType'SYSTEM_DEFINED_ONLY'` and compare with
   Project Admin's operations.
2. Repair the site, open its Networking card.
3. `source lab/env.sh && LAB_PRISM_HOST=portico LAB_JUMP=<host with a
   route to the routed prefix> lab/portico/ready-network.sh <site>`; a
   router with the route and NAT makes the Internet line pass.
