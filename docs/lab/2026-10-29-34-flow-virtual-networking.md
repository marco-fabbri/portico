# 34 — Flow Virtual Networking on both sites (in progress)

## Question

Tenant networking for v0.2.0: Portico gives each tenant a ready network on
every site, following Nutanix's SP Central design (NVD-2219): a VPC per
tenant attached to a NAT external subnet, an internal subnet with Prism
Central's DHCP, and floating IPs. Before designing it: what does a site need
for that, what does enabling Flow Virtual Networking cost, and which data
must Portico ask the MSP for?

## Measured on

- Prism Central: pc.7.6.0.6 on NTNX-01 (`10.23.210.175`, site MI1) and on
  NTNX-02 (`10.23.210.185`, site NYC1), AOS 7.6
- Date: 2026-10-29
- By: Marco (enablement in the Prism Central UI), Claude (reads and the
  external subnets, with Marco's agreement)

## Method

- Flow Virtual Networking enabled by Marco from the Prism Central UI on both
  Prism Centrals, about 05:13 (NTNX-02) and 05:15 (NTNX-01) UTC.
- Tasks read with `GET /api/prism/v4.0/config/tasks?$orderby=createdTime
  desc`; the Prism Central VM read with `POST /api/nutanix/v3/vms/list`.
- Free addresses on VLAN 210 checked with `arping -c 2 -w 3` from the build
  host (`10.23.210.192`, same VLAN); the check was validated on `.175`,
  `.191` and `.254`, which answer.

## Result

- MEASURED: enabling Flow Virtual Networking starts, on each Prism Central,
  `kPrismCentralResize` (with `VmUpdate`), then `kNetworkControllerCreate`
  and `kGlobalNetworkControllerCreate`. After the resize each Prism Central
  VM has 8 vCPU and 31 GiB of memory (size before: not recorded).
- MEASURED: VLAN 210 has no answer to ARP from `.193` to `.253` (2026-10-29).
- MEASURED: NTNX-01 and NTNX-02 sit on the same VLAN 210, so the IP pools
  of their two external subnets must not overlap.
- Earlier the same morning NTNX-01's login page showed "Authentication
  providers not registered yet" for a few minutes while the API answered
  200; `GET /api/iam/v4.1.b3/authn/login-providers` then listed `local` and
  the service-account provider, as on NTNX-02. Cause not found.
- MEASURED: both Prism Centrals run Flow in the **Integrated mode**: the
  enablement resized the Prism Central VM and ran `kNetworkControllerCreate`
  (Flow Virtual Networking Guide 7.6.0: Integrated mode runs the Network
  Controller, formerly ANC, as containers on the Prism Central VMs and needs
  2 vCPU and 3 GB more per VM on a small Prism Central, 3 vCPU and 4 GB on a
  large one; Standalone mode runs the Flow Controller on a Services
  Microservices Platform cluster on the AHV hosts instead). The 8 vCPU and
  31 GiB measured match a small Prism Central plus that increment.
- MEASURED: NTNX-01 finished at about 05:21 UTC (`kGlobalNetworkControllerCreate`
  succeeded); right after, one read of its v4 API got no answer within
  20 s and the next one answered 200.
- From the guide, prerequisites Portico can check or document: not
  supported on an extra small Prism Central; Prism Central registered to the
  cluster that hosts it, and that cluster can no longer be unregistered;
  a Prism Central virtual IP that must not change; the same compatible AHV
  version on every cluster (otherwise the alert "Failed to configure host
  for Atlas networking").
- MEASURED: on NTNX-01 `GET /api/networking/v4.4/config/controllers`
  answers one controller, `controllerStatus` `UP`, `controllerVersion`
  `7.6.0`, `minimumAHVVersion` `11.2`, `defaultVlanStack` `LEGACY`. The
  networking API answers on `v4.0` to `v4.4` and 404 on `v4.5`; the public
  Go client `networking-go-client/v4.4.2` (in `kb/go-client/networking`)
  matches it.
- MEASURED: each site has one subnet, `MGMT`: VLAN `0` (untagged on the
  host's virtual switch, which carries VLAN 210), not external, not
  advanced, no IP address management. No VPC, no floating IP.
- From the guide: an existing VLAN Basic subnet cannot be turned into an
  external one, and the same VLAN must not be both an external network and
  an AHV IPAM network. `MGMT` has no IPAM, so an external subnet on VLAN 0
  beside it is to be measured, not assumed.
- From the guide: the built-in role VPC Admin can create VPCs and subnets
  but not VLAN external subnets (Network Infra Admin can); an external
  subnet is given to a project with "Manage Project Sharing", which the v4
  API exposes as `POST /api/networking/v4.4/config/subnets/{extId}/$actions/share`
  (the same action exists for VPCs).
- MEASURED: `lab/network/external-subnet.sh external-nat 10.23.210.200
  10.23.210.219` created on NTNX-01, at 05:27 UTC, the NAT external subnet
  `external-nat` (VLAN 0, `10.23.210.0/24`, gateway `.254`, pool `.200` to
  `.219`) on `MGMT`'s cluster and virtual switch. Prism Central accepted it
  beside `MGMT` on the same VLAN and marked it `isAdvancedNetworking`
  (Network Controller-based). Right after, Prism Central and the appliance
  on `MGMT` still answered. The pool was checked free with arping just
  before.
- MEASURED: on NTNX-02 the first `kGlobalNetworkControllerCreate`
  (05:13 UTC) failed after 20 minutes with "Global network controller
  create failed: Package could not be deployed on microservices platform";
  Prism Central started a new one by itself at 05:33 (nobody retried it)
  while `kNetworkControllerCreate` stayed
  running. The second attempt failed at 05:54 with the same message, again
  exactly 20 minutes after it started (a timeout), and a third one started
  by itself. NTNX-01 was idle by then, so two Prism Centrals sharing the
  slow Internet link (about 470 KB/s, write-up 19) is an unlikely cause.
  Both run Prism Central 7.6.0.6 on a one-node AOS 7.6 cluster. One
  difference found: NTNX-01's Prism Central lists `127.0.0.1` first among
  its name servers (the microservices platform's own DNS), NTNX-02's does
  not. The third attempt (05:54) failed the same way and the parent
  `kNetworkControllerCreate` failed with it: Flow Virtual Networking is not
  enabled on NTNX-02.
- MEASURED (Proxmox, read only): NTNX-02 is VM 502 on proxmox03, whose ZFS
  pool is a raidz1 of three consumer QLC SSDs (Samsung 870 QVO), with about
  57 ms of write wait in a 5-second `zpool iostat` sample; NTNX-01 is VM 501
  on proxmox01, a raidz1 of six of the same Samsung 870 QVO behind a SAS
  controller (corrected later the same day: `lsblk` shows them as `sas`,
  `smartctl -i` shows the model), about 15 ms. On
  NTNX-01's Prism Central `mspctl cluster health` reports every component
  healthy. Hypothesis, not proven: sustained writes on QLC storage make
  the package deployment on NTNX-02 overrun its 20-minute limit.
- Done (Marco's decision, 09:17–09:30 UTC): NTNX-02 moved to proxmox01,
  beside NTNX-01, to test that hypothesis. Order: Prism Central VM shut
  down with `acli vm.shutdown` (off in about 3 minutes); `cluster stop` on
  the CVM (it asks for the literal answer `I agree`); `cvm_shutdown -P now`;
  the AHV host ignored the ACPI shutdown from Proxmox for 5 minutes and was
  stopped with `qm stop` (nothing ran on it any more); the installer ISO
  ejected (Proxmox refuses to migrate a VM with a CD-ROM on node-local
  storage); `qm migrate 502 proxmox01 --with-local-disks` with the VM off,
  about 105 MB/s on the 1 Gbit cluster network (there is no direct 10 Gbit
  link between the two nodes), for about 300 GB of written data.
- MEASURED (times CET): the migration finished at 10:33 after 1 h 03 min.
  On proxmox01 the kernel logged three tasks blocked for more than 120 s at
  10:30, while it received the disks. NTNX-02 started at 10:50; its CVM
  answered SSH at 10:52; `cluster start` at 10:58 (the CVM showed
  `Maintenance` for about a minute, then `Up`); the first `acli vm.on` of
  its Prism Central failed with "Failed to send RPC request" because the
  AHV host was not yet connected to Acropolis; the second, at 11:12,
  worked, and its v4 APIs answered from 11:42.
- MEASURED: NTNX-01's Prism Central froze and restarted: its journal stops
  at 11:12:53 CET (five seconds after NTNX-02's Prism Central was powered
  on) and its next boot is at 11:27. At 11:48 proxmox01 had a load average
  of 36 on 32 threads, IO pressure (`/proc/pressure/io`) of 16 % "some" and
  8 % "full" over 5 minutes, VM 501 at about 800 % CPU and VM 502 at about
  600 %; the ZFS write wait stayed around 9 ms. So on this host the storage
  is no longer the limit; CPU is, when both Prism Centrals start together.
  Cause of the restart (watchdog, HA or guest hang) not found yet.
- MEASURED at 12:26 CET, both clusters idle: proxmox01 load average about
  21 on 32 threads, IO pressure 6 % "some" over 5 minutes, each VM about
  8 cores busy; NTNX-01's Prism Central answers 200, its controller is
  `UP` and `mspctl cluster health` reports every component healthy.
  Lesson for running both clusters on one host: never start heavy work
  (a Prism Central boot, a Flow enablement) on both at the same time.
- MEASURED: Marco enabled Flow Virtual Networking again on NTNX-02 at
  12:27 UTC, now on proxmox01. It failed exactly as before: three
  `kGlobalNetworkControllerCreate` attempts, each ending after 20 minutes
  with "Package could not be deployed on microservices platform", then the
  parent `kNetworkControllerCreate` (last line read 13:30 UTC). NTNX-01's
  API answered 200 throughout; proxmox01's load stayed between 19 and 34.
  The storage hypothesis is therefore **disproved**.
- MEASURED (likely cause): `GET /api/prism/v4.2/config/domain-managers`
  is the same on both Prism Centrals except the default gateway: NTNX-01's
  is `10.23.210.254`, NTNX-02's is `10.23.210.183`, which is NTNX-02's
  Data Services IP (held by the CVM, it answers ping but routes nothing).
  NTNX-02's CVM routes through `.254` and reaches the Nutanix portal. So
  NTNX-02's Prism Central reaches only VLAN 210, and a download that
  never completes fits a failure after a fixed 20 minutes. Not yet proven:
  the gateway is to be corrected and the enablement retried.
- Done (Marco's OK), times CET: the gateway corrected with the procedure
  of the Prism Central Guide 7.6 ("Reconfiguring the IP Address and
  Gateway of Prism Central VMs"): `cluster stop` on the Prism Central VM,
  then `external_ip_reconfig` (the network has no IPAM, so the NIC and
  IP-pool steps do not apply). Its prompts need a terminal; answers piped
  on stdin end with `EOF when reading a line` before any change, so they
  were given with `expect`. MEASURED: on a Prism Central **without a
  virtual IP** the script fails in its last step, `post_reconfig`, with
  "Zeus config check failed: invalid cluster external ip": it writes the
  unset `cluster_external_ip` back as an empty string, which the validator
  rejects. By then `ifcfg-eth0` already had the new gateway; Zeus still
  had the old one. Marco chose not to add a virtual IP, so Zeus's
  `default_gateway_ip` alone was changed with `edit-zeus` and a one-line
  editor (an editor that replaces the file, like `sed -i`, is silently
  ignored: edit-zeus reads the file it opened), with a copy of the Zeus
  configuration taken before. The remaining steps of the script
  (`ONBOOT`, `BOOTPROTO`, cloud-init network disabled) were already in
  place.
- MEASURED: the reboot of NTNX-02's Prism Central VM, at 15:15:10, made
  its nested AHV host panic within seconds: "Kernel panic - not syncing:
  Host injected async #PF in kernel mode" (`/var/crash` on the host). The
  AHV host restarted by itself, the CVM and its cluster came back up, the
  Prism Central VM stayed off. proxmox01 had 6.8 GB of its 8 GB swap in
  use (swappiness 60) while 94 GB of memory were available, most likely
  since the migration put the ZFS cache under pressure; KSM was off. A
  guest page that Proxmox has to read back from swap is delivered to the
  nested kernel as an asynchronous page fault, and the nested AHV kernel
  panics when it arrives in kernel mode. NTNX-01's Prism Central freeze at
  11:12 may have the same cause (not verified).
- Done (Marco's OK): on proxmox01 `vm.swappiness = 1` in
  `/etc/sysctl.d/91-nestedahv-swappiness.conf`, then `swapoff -a && swapon
  -a` (31 s, swap back to 0). Rule for nested Nutanix on one host: the
  physical host must never swap guest memory.
- MEASURED: NTNX-02's Prism Central powered on again at 15:31; its route
  goes through `.254` and the Nutanix portal answers 200 from it.
- MEASURED: Marco enabled Flow Virtual Networking on NTNX-02 again at
  16:03 UTC, with the gateway corrected: `kGlobalNetworkControllerCreate`
  succeeded in 2 min 30 s (16:03:20 to 16:05:50), where every earlier
  attempt had stopped at 20 minutes; `kNetworkControllerCreate` succeeded
  after it, and the controller answers `UP`, `7.6.0`, minimum AHV `11.2`.
  The cause was the gateway: without Internet access the package of the
  controller cannot be fetched. During the deployment proxmox01 had a load
  of about 25 on 32 threads with CPU pressure under 1 %, IO pressure about
  10 % "some", a ZFS write wait of about 30 ms, no swap in use, and no
  blocked task.
- MEASURED: `lab/network/external-subnet.sh external-nat 10.23.210.220
  10.23.210.239` created NTNX-02's NAT external subnet at 16:13 UTC (VLAN
  0, `10.23.210.0/24`, gateway `.254`, pool `.220` to `.239`, checked free
  with arping from NTNX-02's CVM just before), `isAdvancedNetworking`.
- MEASURED (as `admin`, NTNX-02, 16:26–16:27 UTC): the same ready network,
  `lab/network/tenant-network.sh create portico-netprobe 10.100.1.0/24`,
  in a project made by `lab/projects/create.sh`: share, VPC, overlay
  subnet and floating IP all succeeded in 27 s, with no
  `NETWORKING-10056` since the script shares first. The VPC took `.221`
  and `.222`, the floating IP `.228`. The same tenant `/24` as on NTNX-01
  works, as the design wants. `delete` and `lab/projects/delete.sh` left
  only `MGMT` and `external-nat`, shared with no project.
- MEASURED (as the `portico` service account, NTNX-01, 16:42–16:44 UTC,
  with a temporary API key from `lab/auth/create-service-account.sh`,
  revoked at the end): the account holds only the `Project Manager_acp`
  authorization policy.
  - Reads of subnets, VPCs and controllers answer 200; the read of
    floating IPs answers 403. `lab/projects/create.sh` works.
  - Sharing the external subnet with the tenant's project is refused:
    `NETWORKING-10071` "User denied access to share subnet", and the VPC
    then fails with `NETWORKING-10056`. The external subnet's owner is
    the system project `_internal`, and the guide asks for the right
    networking permissions in the owner project.
  - With the subnet shared by `admin`, the account creates the VPC and the
    overlay subnet in the project; the floating IP is refused ("User denied
    access to create floating IP", the message adds that view permission
    is needed too).
  - The IAM operations involved are `Share_Subnet` and `Unshare_Subnet`
    (on the external subnet) and `View_Floating_Ip`, `Create_Floating_Ip`,
    `Update_Floating_Ip`, `Delete_Floating_Ip`. Whether a custom role
    with these, scoped to the external subnets and the tenants' projects,
    is enough is the next measurement.
- MEASURED (as the `portico` service account, NTNX-01, 20:16 UTC, a
  temporary key): with the role **Portico Networking** created by Marco
  through `lab/auth/networking-role.sh create` (six operations, subnet
  part limited to `external-nat` by `extId`, floating IPs on all), the
  account **shared the external subnet** with its project, where
  `Project Manager_acp` alone was refused, and created the VPC. The
  default route then failed: listing route tables answers 403
  `NETWORKING-10071` "User denied access to list route tables";
  `Project Manager_acp` has no route table operation either. The role
  gains `View_VPC_Route_Table`, `View_Route_Table_Route`,
  `Create_Route_Table_Route` and `Delete_Route_Table_Route` (entity type
  `vpc_route_table`).
- MEASURED (as `admin`, NTNX-01, 19:55–20:10 UTC): a VM on the ready
  network, `lab/network/probe-vm.sh create|check|delete portico-netprobe`
  (Rocky 9 GenericCloud, 1 vCPU, 1 GiB, user `probe` with an SSH key
  through cloud-init, the floating IP associated with its NIC through
  `PUT .../floating-ips/{extId}` with a `VmNicAssociation`):
  - The VM got `10.100.1.22` from the subnet's pool (IP address
    management of Prism Central) and its default route and DNS from the
    VPC's DHCP. The floating IP showed `ASSOCIATED_TO_VNIC`.
  - **Nothing left the VPC.** A capture on the VM's tap interface on the
    AHV host showed a ping to the floating IP arrive translated
    (`10.23.210.192 > 10.100.1.22`) and the VM answer it, but no answer
    left the host's uplink; the VM's DNS queries to `1.1.1.1` got no
    answer. The VPC's route table held only its local route
    (`10.100.1.0/24`): **a VPC created through the v4 API gets no default
    route**, unlike the UI, which offers one when an external subnet is
    attached.
  - With a static route `0.0.0.0/0` → `EXTERNAL_SUBNET` (`external-nat`),
    `POST .../route-tables/{extId}/routes`, the ping answered at once;
    over SSH on the floating IP, from the build host and from the Mac,
    the VM had its address, its route through `.1`, `1.1.1.1` as name
    server, and `https://dl.rockylinux.org/` answered 200 in 0.65 s
    through the VPC's SNAT. `tenant-network.sh` now adds the route right
    after the VPC.
  - With the VM still on the subnet, `tenant-network.sh delete`: the
    floating IP **was deleted although associated with the VM** (Prism
    Central does not protect it); the subnet and the VPC were refused
    (`NETWORKING-10068` "1 subnets still configured" for the VPC), and so
    was `unshare` ("Subnet ... is attached to a VPC"). After the VM was
    deleted the same run removed everything, the VPC with its static
    route, and the external subnet was shared with no project.
- MEASURED (as `admin`, NTNX-01, 20:35–20:45 UTC, Marco's OK): NVD-2219's
  shared transit model, `lab/network/transit.sh`:
  - The MSP's side: a VPC of type `TRANSIT` on `external-nat` (it took
    `.204` and `.205`), its static default route to `external-nat` (the
    API adds none here either), and a NAT overlay external subnet
    `portico-transit-oen` (`isExternal`, `isNatEnabled`,
    `100.64.0.0/24`) in it; each one call and one task.
  - The tenant's side, `EXT_SUBNET=portico-transit-oen
    lab/network/tenant-network.sh create portico-transitprobe
    10.100.2.0/24`: the same five steps succeeded unchanged (share,
    VPC, default route to the overlay external subnet, overlay subnet,
    floating IP). The tenant VPC took one address of the overlay pool
    (`100.64.0.114`) instead of two of the VLAN, and the floating IP is
    `100.64.0.99`, an address inside the transit model, not reachable
    from the VLAN.
  - Publishing the VM takes a **second floating IP, on the transit
    VPC**: one of `external-nat` associated with the private IP
    `100.64.0.99` in the transit VPC (`PrivateIpAssociation`), status
    `ASSOCIATED_TO_PRIVATE_IP`, `10.23.210.215`. Over SSH on it from the
    Mac the VM had its DHCP address `10.100.2.156` and reached the
    Internet (200 in 0.49 s) through both NATs.
  - Removal in reverse left only `MGMT` and `external-nat`.
- MEASURED (NTNX-01, 2026-10-29, as a tenant): `lab/projects/probe-user.sh
  create portico-netprobe` made a local user who is Project Admin of that
  project only (role membership with `ProjectsScopeTemplate`), and the
  tenant's calls ran as that user:
  - Lists of VPCs, subnets and floating IPs show only the project's own
    objects plus the external subnet shared with it; other projects'
    objects and `MGMT` are not listed.
  - The user created the VM on the project's subnet and associated the
    project's floating IP with its NIC (`VmNicAssociation`): Project
    Admin is enough for both, no custom tenant role is needed.
  - The user can also **create and delete floating IPs of its own** on
    the shared external subnet: the number Portico assigns is not a
    limit by itself.
  - The cluster list is refused (403), so a VM create needs the cluster
    given (`CLUSTER` in `lab/network/probe-vm.sh`); the route table is
    listed only with `$filter=vpcReference eq '...'` (without the filter
    the list is empty).
  - `probe-user.sh delete` removed the role membership, the user and its
    password file.
- MEASURED (NTNX-01, 2026-10-29, as `admin`): two tenants on **one shared
  external subnet** cannot be disconnected one at a time. With
  `portico-twoprobe-a` and `-b` each holding a ready network on
  `external-nat`, removing A's floating IP, subnet and VPC succeeded, but:
  - `unshare` for A was refused, `NETWORKING-10068` "Failed to
    Share/Unshare Subnet with projects as the referenced resource is
    still in use - Subnet ... is attached to a VPC": B's VPC, in another
    project, is enough to block it;
  - `lab/projects/delete.sh` for A was then refused: the project still
    has a resource group and a shared subnet
    (`associatedEntities` `{"resource groups":1,"shared subnets":1}`),
    and deleting the resource group directly fails "not empty: 10018";
  - a `PUT` of the subnet with A removed from `sharedWithProjects` is
    accepted and ignored.
  Only after B's VPC was gone did A's `unshare` and project delete
  succeed. The same lock probably explains the old project
  `portico-lab-tenant-a`, still shared on `MGMT`.
- MEASURED (NTNX-01, 2026-10-29, as `admin`): with **one overlay external
  subnet per tenant** in the transit VPC the lock is gone.
  `OEN_NAME=portico-oen-a OEN_CIDR=100.64.1.0/24 lab/network/transit.sh
  create`, the same for `-b` on `100.64.2.0/24`, then each tenant's five
  steps with `EXT_SUBNET` set to its own subnet (all succeeded). The
  Disconnect of A with B active removed A's floating IP, subnet and VPC,
  `unshare` succeeded, the project was deleted, and A's overlay external
  subnet was removed; the transit VPC stayed for B and went with B's
  subnet.
- MEASURED (NTNX-01, 2026-10-29 evening, as `admin`): NVD-2219's routed
  form of the shared transit model, where the NAT is in each tenant's
  overlay external subnet and the transit VPC is routed:
  - A no-NAT VLAN external subnet next to `external-nat` on VLAN 210 was
    refused, `NETWORKING-10056` "Managed subnet exists with VLAN ID 0 on
    same DVS": one managed subnet per VLAN and virtual switch on a
    cluster. `external-nat` (unused) was deleted and `external-routed`
    created in its place (`EXT_NAT=false lab/network/external-subnet.sh
    external-routed 10.23.210.200 10.23.210.219`).
  - The transit VPC with a fixed address was refused until it asked for
    one active gateway: "The number of external IP addresses must be
    equal to active gateway nodes"; with `activeGatewayCount` 1 it took
    `10.23.210.200`, the externally routable prefix `10.23.211.0/24`
    and its default route (`EXT_SUBNET=external-routed
    TRANSIT_IP=10.23.210.200 ERP=10.23.211.0/24 lab/network/transit.sh
    create`).
  - The tenant's overlay external subnet `10.23.211.16/28` (gateway
    `.17`, pool `.18` to `.30`), then the tenant's five steps on it: the
    VPC took `10.23.211.29` for SNAT, the floating IP `10.23.211.18`.
  - The first create of the tenant's internal subnet returned
    `SUCCEEDED` but the subnet had no `projectExtId` although the request
    carried it; the VM create then failed "given subnet ... is not
    found". Deleted and created again, the subnet had its project and the
    VM was created. One create out of three in this write-up behaved so.
  - With a static route `10.23.211.0/24 via 10.23.210.200` on the build
    host only, the VM was reached over SSH on `10.23.211.18` from the
    build host, with its DHCP address `10.100.11.39`: **one floating IP**
    publishes the VM, as in the NVD. Its Internet access failed, as
    expected while the upstream router has no route back to the prefix
    and no NAT rule for it.
- MEASURED (read only, both sites): the networking controller's API has
  no field for the Flow mode (`controllers` lists version, status,
  health per cluster and node, the VLAN stack, nothing about Integrated
  or Standalone). Prism Central's gateway is read from
  `GET /api/prism/v4.2/config/domain-managers`,
  `network.externalNetworks[].defaultGateway`, now `.254` on both; the
  cluster's own addresses from `GET /api/clustermgmt/v4.0/config/clusters`
  (`externalAddress`, `externalDataServiceIp`, and per node the CVM and
  host addresses). NYC1's old gateway `.183` is its cluster's
  `externalDataServiceIp`: a check "the gateway is not one of the
  cluster's addresses" would have caught it.
- MEASURED: Marco enabled Flow Network Security on NTNX-01 at 05:37 UTC:
  `kPrismCentralResize`, then `kMicrosegEnablementAsync` and
  `kEnableMicroseg`, all succeeded within a minute; the Prism Central VM
  went from 31 to 33 GiB of memory, still 8 vCPU.
- MEASURED (as `admin`, NTNX-01, 05:39–05:45 UTC): a tenant's ready network
  is built by `lab/network/tenant-network.sh create portico-netprobe
  10.100.1.0/24` in a project made by `lab/projects/create.sh`:
  1. The first VPC create was refused: `NETWORKING-10056` "Cannot
     create/update entity because the following dependencies are outside
     the project boundary: External subnet ... does not belong to or is not
     shared with project ...". The external subnet must first be shared with
     the tenant's project: `POST /api/networking/v4.4/config/subnets/{extId}/$actions/share`
     with `{"projectExtId": ...}` and the subnet's ETag in `If-Match`.
  2. Then the VPC (`vpcType` `REGULAR`, `projectExtId`, one
     `externalSubnets` entry, `commonDhcpOptions.domainNameServers`), the
     overlay subnet `10.100.1.0/24` (gateway `.1`, pool `.10` to `.250`,
     `projectExtId`, `vpcReference`) and an unassociated floating IP
     (`projectExtId`, `externalSubnetReference`) were each created by one
     v4 call and one task.
  3. The VPC took two addresses of the external pool (`.214`, `.215`), the
     floating IP one (`.213`). All three entities carry the project's
     `projectExtId`. (NTNX-02 below allocated from the middle of its pool:
     the order is not fixed, so Portico must read the addresses, not
     predict them.)
  4. `delete` removed the floating IP, the subnet and the VPC, then
     `unshare` gave the external subnet back (`sharedWithProjects` empty),
     and `lab/projects/delete.sh` removed the project.
- Reported in a public SP Central installation write-up (2026): SP Central
  and Nutanix Central support only the Integrated mode for now. The SP
  Central 1.0 guides in `kb/` do not mention the modes; as prerequisites
  they list the Flow Virtual Networking ports for the controller and the
  gateways, Prism Central not extra small, and the policy engine with Flow
  enabled for tenant policy enforcement and RBAC evaluation. Portico
  follows SP Central here: Integrated mode is the supported one, so the
  choice between modes stays with the MSP and Portico only reads it.
- From the Flow Network Security 7.6 release notes: project-scoped
  security entities (policies, address, service and entity groups owned by
  one project, with RBAC evaluated in that project) and FQDN rules work in
  both modes; the rule-centric policy model and the flow analytics need the
  Standalone mode. Portico's tenant isolation needs only the first, so the
  Integrated mode is enough; the Standalone mode must simply not break it
  (the Flow Management Reference Guide, not in `kb/`, has its details).

## Data Portico must ask for, or read

Collected while preparing the sites, so the v0.2.0 forms and checks ask for
everything at once. "Read" means Portico finds it in Prism Central and the
MSP only confirms or picks.

| data | where | asked or read | notes |
|---|---|---|---|
| Flow Virtual Networking enabled | per site | read | prerequisite; without it the site works but its ready network stays off |
| Flow mode (Integrated or Standalone) | per site | read | Integrated required, as for SP Central, which does not support Standalone yet; a Standalone site is refused for the ready network until measured |
| policy engine enabled | per site | read | needed for project quotas (Prism Central Admin Center Guide 7.6) and listed by the SP Central guides; it deploys one VM and needs a free IP on Prism Central's network; to be measured on both sites |
| Flow Network Security enabled | per site | read | NVD-2219 prerequisite with Flow Virtual Networking; Marco enabled it on 2026-10-29 after the networking |
| Prism Central size and virtual IP | per site | read | extra small is not supported; the guide lists a virtual IP, but Flow Virtual Networking was enabled on both single-VM Prism Centrals without one (measured), and Marco keeps them without; a scale-out Prism Central has one, and Portico follows a new address through the site's Connection card |
| AHV versions of the site's clusters | per site | read | must be the same compatible version |
| external subnet for tenant traffic | per site | read, MSP picks | NAT external subnet; the MSP creates it in Prism Central |
| its VLAN, CIDR, gateway | per site | read | from the picked subnet, shown for confirmation; the VLAN is the one the AHV host sees (`0` when the uplink is untagged) |
| sharing of the external subnet with the tenant's project | per tenant and site | done by Portico | required before the VPC (`NETWORKING-10056` otherwise); `$actions/share`, and `unshare` at Disconnect, which fails while any other VPC uses the same external subnet (measured) |
| overlay external subnet of the tenant (transit model) | per tenant and site | done by Portico | one per tenant in the MSP's transit VPC, so the tenant's Disconnect is not blocked by the others; its `/24` from a transit pool, e.g. `100.64.<n>.0/24` |
| service account's networking role | per site | prerequisite or created by Portico | Project Manager is not enough: it lacks `Share_Subnet`/`Unshare_Subnet` on the external subnet (owned by `_internal`) and the floating IP operations (measured); a custom role with them is to be measured |
| its IP pool (SNAT and floating IPs) | per site | read | two addresses per VPC on a one-node cluster (measured), one per floating IP; pools of sites on the same VLAN must not overlap; Portico can warn when the pool runs low |
| DNS servers for tenant subnets | per site | asked | given to the tenant subnet's DHCP |
| tenant address pool | MSP setting | asked | e.g. `10.100.0.0/16`, one `/24` per tenant, the same on every site |
| tenant number | per tenant | assigned by Portico | assigned at creation, never reused |
| tenant subnet exception | per tenant | asked, optional | when the default `/24` collides with the customer's own network |
| floating IPs per tenant | per tenant | asked | number allowed; a Project Admin can create its own floating IPs on the shared external subnet (measured), so the number is enforced by Portico or by the tenant's role, not by the assignment alone |
| Prism Central gateway and Internet access | per site | read, checked | the gateway from `domain-managers` must be the subnet's router; a wrong one (NTNX-02 had the Data Services IP) breaks Flow enablement, which downloads packages, while everything else on the VLAN works |
| Prism Central capacity | per site | prerequisite | Flow Virtual Networking adds 2 vCPU and 3 GB per VM on a small Prism Central, Flow Network Security about 2 GB more (8 vCPU, 33 GiB measured after both) |
| stretch of the tenant subnet between sites (optional) | per tenant | asked | off by default; needs the same tenant `/24` on both sites (measured: it works) |
| latency and bandwidth between the sites | per site pair | measured or asked | only for the stretch and live migration: RTT under 5 ms and bandwidth for peak writes for synchronous replication and CCLM (DR Guide 7.6); on-demand CCLM needs no synchronous replication but copies memory and disks |
| availability zone pairing of the two Prism Centrals | per site pair | prerequisite | for the Create Subnet Extension wizard (Flow Virtual Networking Guide 7.6) |
| AHV version on both sites | per site pair | read | must be the same for CCLM |
| addresses for the Network Gateway appliance | per tenant and site | asked or from the pool | the stretch runs through a gateway VM per extension; the VPC also needs a `0.0.0.0/0` static route to the external network |
| firewall ports for the controller and gateways | per site | prerequisite | listed in the Flow Virtual Networking Guide |

## Implications for the design

- A site's ready network has four prerequisites Portico can read before it
  offers the feature: Flow Virtual Networking with a controller `UP`
  (`GET /api/networking/v4.4/config/controllers`), the Integrated mode,
  a NAT external subnet the MSP created and picks, and a Prism Central
  whose gateway is the router of its own subnet (`domain-managers`). The
  last one is cheap to check and costly to miss: everything else on the
  VLAN works and only the Flow enablement fails, after an hour.
- Portico does not enable Flow, create the external subnet or pick the
  Flow mode: those are platform choices of the MSP, as in SP Central.
  Portico reads them, explains what is missing, and keeps the ready network
  off on a site that lacks them.
- The ready network of one tenant on one site is five v4 calls, each one
  task: share the external subnet with the tenant's project, the VPC, its
  default route to the external subnet (without it nothing leaves the
  VPC, measured), the overlay subnet, one floating IP; 27 to 30 s
  measured. Removal is the reverse plus `unshare`; the route goes with
  the VPC. A VM on it gets its address by DHCP, reaches the Internet
  through SNAT and is reached on its floating IP (measured).
- Prism Central deletes a floating IP associated with a VM without a
  word, so Portico must check the association itself before deleting
  one.
- The transit model needs nothing else from the tenant's steps: the
  same calls work with the overlay external subnet in place of the VLAN
  one. Its cost is on the publishing side: a tenant's floating IP is an
  overlay address, and a second floating IP of the VLAN on the MSP's
  transit VPC makes it reachable; that one belongs to no tenant project. Each step is idempotent by name, so it fits the tenant
  steps Portico already records and retries.
- An external subnet shared by several tenants cannot be given back by one
  of them while another tenant's VPC uses it, and the project cannot be
  deleted meanwhile (measured). With the VLAN external subnet shared by
  every tenant, only the last tenant could ever be disconnected. In the
  transit model Portico gives each tenant its own overlay external
  subnet in the transit VPC: the Disconnect removes it with the tenant,
  and the others are untouched (measured). The direct VLAN model would
  need one VLAN external subnet per tenant, which costs a VLAN or a pool
  slice each; it stays possible but is not the recommended one.
- Project Admin is enough for the tenant to run VMs on the ready network
  and associate its floating IP (measured), so the tenant role Portico
  creates is not needed for that. It also lets the tenant create floating
  IPs of its own; limiting them takes a tenant role without
  `Create_Floating_Ip`, or a check in Portico's monitor.
- Portico reads back the `projectExtId` of every object it creates:
  Prism Central once created a subnet without its project although the
  request carried it, and reported success (measured).
- The NVD's routed transit is what Portico builds: a no-NAT VLAN external
  subnet for the transit VPC, a routed prefix the MSP's router sends to
  the transit VPC's fixed address, a `/28` of it per tenant, one floating
  IP per published VM. The NAT transit VPC with a second floating IP
  works too (measured) but is not the NVD's model.
- Portico reads the addresses Prism Central assigns (two per VPC, one per
  floating IP) instead of predicting them: the order is not fixed.
- The same tenant `/24` works on every site, so one MSP address pool with
  one `/24` per tenant number is enough; an exception per tenant covers a
  collision with the customer's own network.
- Project Manager is not enough for the service account (measured): it
  cannot share the external subnet, owned by the system project
  `_internal`, nor touch floating IPs. Portico needs a networking role on
  top, with `Share_Subnet` and `Unshare_Subnet` on the external subnets
  and the floating IP operations. `lab/auth/networking-role.sh` creates
  one; whether it is enough, and whether the floating IP part can be
  limited to the tenants' projects, is measured next.
- Subnet stretch and cross-cluster live migration stay an optional,
  per-tenant feature for later (backlog): they need an availability zone
  pairing, a gateway VM per extension, and bandwidth Portico cannot create.
- Running both nested clusters on one Proxmox host works, under two rules
  that are not Portico's but cost hours when ignored: the host never swaps
  guest memory, and heavy work (a Prism Central boot, a Flow enablement)
  never starts on both clusters at once.

## How to re-measure after an upgrade

1. On each site: `GET /api/networking/v4.4/config/controllers` (controller
   `UP`, version, minimum AHV) and `GET /api/prism/v4.2/config/domain-managers`
   (gateway). If the networking API version moved, compare with the Go
   client in `kb/go-client/networking`.
2. `source lab/env.sh` (with `PC_URL` set to the site: `lab/env.sh` reads
   `nyc1-pw` for NYC1, `prism-pw` otherwise), check the pool free with
   arping, then `lab/network/external-subnet.sh NAME FIRST LAST` if the
   external subnet is missing.
3. As `admin`: `lab/projects/create.sh`, then `lab/network/tenant-network.sh
   create|show|delete portico-netprobe 10.100.1.0/24`, then
   `lab/projects/delete.sh`. Compare the steps, the codes and the time with
   the results above. In between, `lab/network/probe-vm.sh create|check|delete
   portico-netprobe` checks the data path (DHCP, SNAT, floating IP).
4. As the tenant: `lab/projects/probe-user.sh create portico-netprobe`, the
   tenant's calls with `PC_USER=portico-probe-user` and the generated
   password file (lists, VM with `CLUSTER`, floating IP association and
   creation), then `probe-user.sh delete`.
5. Two tenants: `lab/network/transit.sh create` with `OEN_NAME` and
   `OEN_CIDR` for each, the tenants' `create` with their own `EXT_SUBNET`,
   then the first tenant's `delete` and `lab/projects/delete.sh` while the
   second is active; both must succeed. Repeat with both tenants on
   `external-nat` to see whether the lock is still there.
6. As the service account: `lab/auth/create-service-account.sh` for a key,
   `lab/auth/networking-role.sh create` (it refuses if an operation was
   renamed by the upgrade), the same run with `PC_API_KEY` set, then
   `lab/auth/networking-role.sh delete` and the key revoked.
