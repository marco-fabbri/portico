# 38 — What the peering lets Portico change

## Question

The prefix routing design (2026-11-01, section 6) leaves to a measurement
what Portico may change in place and what it must rebuild: a BGP
session's gateway references, a remote gateway's address, an external
subnet's gateway while a transit VPC uses it, an image imported from a
URL, a VM with its NIC on the external subnet itself, how Prism Central
reports a session whose peer is gone, and which IAM operations the
service account needs for gateways, sessions, images, VMs and categories.

## Measured on

- Prism Central: pc.7.6.0.6 on NTNX-01 (`10.23.210.175`), AOS 7.6, Flow
  Virtual Networking in the Integrated mode; the peering of write-up 37
  (`portico-transit-bgp` `.195`, `portico-edge` `.194`, session
  `portico-transit-to-edge`)
- Date: 2026-11-01
- By: Claude

## Method

`lab/network/bgp-gateway.sh` gained the verbs `update-session`,
`update-remote`, `subnet-gateway`, `image-url`, `edge-on-external`,
`delete-vm`, `delete-image` (each a GET, the change, a PUT with the ETag
or a POST, and the task waited). Run as `admin` through `lab/pc.sh`:

1. A second remote gateway `portico-edge-2` (`.196`, ASN 65021), then
   `update-session <local> <portico-edge-2>`.
2. `update-remote 10.23.210.196` on `portico-edge`, the session and FRR
   read after 45 s, then `update-remote 10.23.210.194`.
3. `subnet-gateway external-routed 10.23.210.254`, then back to `.194`.
4. `image-url portico-edge-urltest <Rocky 9 GenericCloud qcow2 URL>`.
5. The IAM operations catalogue (`GET /api/iam/v4.0/authz/operations`,
   1397 entries) filtered for the entity types the design needs; the
   service account's own calls are checked by Repair in the acceptance
   (write-up 39): the temporary key this measurement planned was not
   created (the session's tooling refused the step).
6. `portico-edge` powered off for ten minutes, the session and the
   gateway read every minute, then powered on and read every 30 s.
7. A VM `portico-exttest` with its NIC on `external-routed`, three
   bodies: a requested address `.197/32`, `shouldAssignIp` false, no
   `ipv4Config`.

Everything created was deleted afterwards; the peering is as write-up 37
left it.

## Result

- MEASURED (1): `PUT /bgp-sessions/{extId}` with another
  `remoteGatewayReference` is refused: `NETWORKING-10056 … remote_gateway_uuid
  is immutable`. The error names the remote reference; the local one is
  taken as immutable too (one local gateway in the lab, not tested). A
  change of peer object is delete and create of the session.
- MEASURED (2): `PUT /gateways/{extId}` on a remote gateway with a new
  address is accepted (task SUCCEEDED) and the session follows at once:
  with `.196` (nobody there) the session reports `DOWN, "Password/ASN
  Mismatch or BGP not configured on Peer. Current BGP state: Active"`
  and FRR on `.194` sees the neighbour `Active`, 0 prefixes; back at
  `.194` the session is `UP` within 45 s and FRR receives the prefix
  again. A move of the peer's address needs no session rebuild.
- MEASURED (3): `PUT /subnets/{extId}` changing the external subnet's
  `defaultGatewayIp` while the transit VPC uses it is accepted both ways
  (`.254`, then `.194`), task SUCCEEDED each time; the subnet's pool and
  the VPC's attachment are unchanged. The design's 4.3 stands: the
  external subnet's gateway moves with the edge.
- MEASURED (4): `POST /api/vmm/v4.0/content/images` with
  `source.$objectType vmm.v4.content.UrlSource` imports a 1 GiB qcow2
  from the Internet in 184 s on this site; the image is `DISK_IMAGE`
  under the given name.
- MEASURED (5): the operations the service account needs, by entity
  type: `View_Network_Gateway`, `Create_Network_Gateway`,
  `Update_Network_Gateway`, `Delete_Network_Gateway` (`network_gateway`);
  `View_Bgp_Session`, `Create_Bgp_Session`, `Update_Bgp_Session`,
  `Delete_Bgp_Session`, `View_Bgp_Route` (`bgp_session`); `View_Image`,
  `Create_Image`, `Delete_Image` (`image`); `View_Virtual_Machine`,
  `Create_Virtual_Machine`, `Update_Virtual_Machine`,
  `Delete_Virtual_Machine`, `Allow_Virtual_Machine_Power_On`,
  `Allow_Virtual_Machine_Power_Off`, `Update_Virtual_Machine_Categories`
  (`vm`); `View_Category`, `Create_Category` (`category`);
  `Update_Subnet` (`subnet`, for the external subnet's gateway);
  `View_Cluster`. Whether this set is sufficient and minimal is checked
  by the acceptance's Repair (write-up 39).
- MEASURED (6): the session reports `DOWN` about 3.5 minutes after the
  edge goes away (the BGP hold time of 180 s, then the next read), with
  the text `Password/ASN Mismatch or BGP not configured on Peer. Current
  BGP state: Active`; the gateway stays `UP` throughout; the session is
  `UP` again 2 min 19 s after the edge is powered on (its boot and FRR),
  with no action in Prism Central:

  | elapsed | edge | session | gateway |
  |---|---|---|---|
  | 0:00 | powered off (task SUCCEEDED) | UP | UP |
  | 1:13, 2:23 | off | UP | UP |
  | 3:25 to 12:26 | off | DOWN, peer `Active` | UP |
  | 12:28 | powered on | DOWN | UP |
  | 13:14 to 14:16 | booting | DOWN, peer `Active` | UP |
  | 14:47 | up | UP, "bgp session is up" | UP |

  The card's monitor therefore sees a peer gone within about four
  minutes, and a replace of the edge (about three minutes without it)
  shows as one `DOWN` window.
- MEASURED (7): a normal VM NIC on the external subnet is refused by the
  VM create task, with and without a requested address: `VMM-30102
  "Failed to perform the operation on the VM with UUID 'EMPTY', due to
  an invalid argument"`. The gateway's and the edge's NICs go on an
  unmanaged VLAN subnet of the same VLAN (`MGMT` in the lab); where none
  exists, Portico creates `portico-<site>-vlan` (design, section 3).

## Implications for the design

- 4.3 and 4.5: a change of the peer's address is a PUT of the remote
  gateway, never a session rebuild; a change of peer object (another
  remote gateway) is delete and create of the session. The design's
  "or recreated and the session updated" path is dropped.
- Section 3: the NIC placement is decided: always an unmanaged VLAN
  subnet, created by Portico when the site has none on the external
  subnet's VLAN and virtual switch; the external subnet is never a VM's
  subnet.
- 4.7: the operations list above goes into `networking_rights.go`;
  `Update_Subnet` joins the ready network's own list.
- 4.2: a session `DOWN` carries Prism Central's text of the peer's BGP
  state, which the card shows as it is; a peer gone is reported after
  the BGP hold time (about 3.5 minutes), not at once.

## How to re-measure after an upgrade

```
source lab/env.sh
REMOTE_NAME=portico-edge-2 REMOTE_IP=10.23.210.196 REMOTE_ASN=65021 lab/network/bgp-gateway.sh create
lab/network/bgp-gateway.sh update-session <local extId> <portico-edge-2 extId>   # expect NETWORKING-10056
lab/network/bgp-gateway.sh update-remote 10.23.210.196; lab/network/bgp-gateway.sh update-remote 10.23.210.194
lab/network/bgp-gateway.sh subnet-gateway external-routed 10.23.210.254; lab/network/bgp-gateway.sh subnet-gateway external-routed 10.23.210.194
lab/network/bgp-gateway.sh image-url portico-edge-urltest <qcow2 URL>; lab/network/bgp-gateway.sh delete-image portico-edge-urltest
lab/network/bgp-gateway.sh edge-on-external portico-exttest 10.23.210.197/24 10.23.210.194   # expect VMM-30102
REMOTE_NAME=portico-edge-2 LOCAL_NAME=__none__ SESSION_NAME=__none__ lab/network/bgp-gateway.sh delete
```
