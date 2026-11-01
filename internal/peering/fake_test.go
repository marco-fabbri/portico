package peering

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

// fakePC is the part of Prism Central the peering service talks to, in
// memory: objects by extId, a call log for order assertions, the local
// gateway's and the session's states answered in sequence (the last one
// repeats), and the refusals the measurements found.
type fakePC struct {
	gatewayErrs     []error // answers to the next reads of a local gateway, before its states
	sessionErrs     []error // answers to the next reads of a session, before its states
	updateRemoteErr error   // what UpdateRemoteGateway answers when set
	mu              sync.Mutex
	calls           []string
	gateways        map[string]nutanix.Gateway
	sessions        map[string]nutanix.BGPSession
	vms             map[string]nutanix.VM
	subnets         map[string]nutanix.Subnet
	vpc             nutanix.VPC
	gatewayStates   []string
	sessionStates   []string
	sessionMsg      string
	refusePut       bool
	routes          []nutanix.BGPRoute
	next            int
}

const (
	systemProject = "00000000-0000-0000-0000-000000000000"
	extSubnet     = "ext-1"
	mgmtSubnet    = "mgmt-1"
)

func newFakePC() *fakePC {
	f := &fakePC{gateways: map[string]nutanix.Gateway{}, sessions: map[string]nutanix.BGPSession{}, vms: map[string]nutanix.VM{}, subnets: map[string]nutanix.Subnet{},
		gatewayStates: []string{"UP"}, sessionStates: []string{"UP"}}
	f.vpc = nutanix.VPC{ExtID: "vpc-1", Name: "portico-transit", External: []nutanix.VPCExternal{{SubnetExtID: extSubnet, IPs: []netip.Addr{netip.MustParseAddr("10.0.210.200")}}},
		RoutablePrefixes: []netip.Prefix{netip.MustParsePrefix("100.96.0.0/24")}}
	f.subnets[extSubnet] = nutanix.Subnet{ExtID: extSubnet, Name: "external-routed", Type: "VLAN", ClusterExtID: "cl-1", External: true, NAT: false,
		Prefix: netip.MustParsePrefix("10.0.210.0/24"), Gateway: netip.MustParseAddr("10.0.210.254"),
		Pool: [2]netip.Addr{netip.MustParseAddr("10.0.210.200"), netip.MustParseAddr("10.0.210.220")}, VLANID: 210, VirtualSwitchExtID: "vs-1"}
	f.subnets[mgmtSubnet] = nutanix.Subnet{ExtID: mgmtSubnet, Name: "MGMT", Type: "VLAN", ClusterExtID: "cl-1", VLANID: 210, VirtualSwitchExtID: "vs-1"}
	f.routes = []nutanix.BGPRoute{{Prefix: netip.MustParsePrefix("100.96.0.0/24"), NextHop: netip.MustParseAddr("10.0.210.200")}}
	return f
}

func (f *fakePC) log(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakePC) id(prefix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return fmt.Sprintf("%s-%d", prefix, f.next)
}

// pop answers the next state of a sequence, the last one repeating.
func pop(seq *[]string) string {
	if len(*seq) == 0 {
		return ""
	}
	s := (*seq)[0]
	if len(*seq) > 1 {
		*seq = (*seq)[1:]
	}
	return s
}

func (f *fakePC) ListGateways(context.Context) ([]nutanix.Gateway, error) {
	f.log("ListGateways")
	var out []nutanix.Gateway
	for _, g := range f.gateways {
		out = append(out, g)
	}
	return out, nil
}

func (f *fakePC) GetGateway(_ context.Context, extID string) (*nutanix.Gateway, error) {
	f.log("GetGateway %s", extID)
	g, ok := f.gateways[extID]
	if !ok {
		return nil, &nutanix.APIError{Status: 404, Message: "gateway " + extID}
	}
	if g.Local {
		f.mu.Lock()
		if len(f.gatewayErrs) > 0 {
			err := f.gatewayErrs[0]
			f.gatewayErrs = f.gatewayErrs[1:]
			f.mu.Unlock()
			return nil, err
		}
		g.State = pop(&f.gatewayStates)
		f.mu.Unlock()
		if g.State == "DOWN" {
			g.Message = "Service nginx is down"
		}
	}
	return &g, nil
}

func (f *fakePC) CreateLocalBGPGateway(_ context.Context, name, projectExtID, clusterExtID, subnetExtID string, addr netip.Prefix, gw netip.Addr, asn int, vpcExtID string) (*nutanix.Gateway, error) {
	f.log("CreateLocalBGPGateway %s project=%s cluster=%s subnet=%s addr=%s gw=%s asn=%d vpc=%s", name, projectExtID, clusterExtID, subnetExtID, addr, gw, asn, vpcExtID)
	g := nutanix.Gateway{ExtID: f.id("gw"), Name: name, ProjectExtID: projectExtID, Local: true, Address: addr.Addr(), ASN: asn, VMExtID: "vm-gw"}
	f.gateways[g.ExtID] = g
	return &g, nil
}

func (f *fakePC) CreateRemoteBGPGateway(_ context.Context, name, projectExtID string, addr netip.Addr, asn int) (*nutanix.Gateway, error) {
	f.log("CreateRemoteBGPGateway %s project=%s addr=%s asn=%d", name, projectExtID, addr, asn)
	g := nutanix.Gateway{ExtID: f.id("rgw"), Name: name, ProjectExtID: projectExtID, Address: addr, ASN: asn}
	f.gateways[g.ExtID] = g
	return &g, nil
}

func (f *fakePC) UpdateRemoteGateway(_ context.Context, extID string, addr netip.Addr, asn int) error {
	f.log("UpdateRemoteGateway %s addr=%s asn=%d", extID, addr, asn)
	if f.updateRemoteErr != nil {
		return f.updateRemoteErr
	}
	if f.refusePut {
		return &nutanix.APIError{Status: 400, Message: "NETWORKING-10056 address is immutable"}
	}
	g, ok := f.gateways[extID]
	if !ok {
		return &nutanix.APIError{Status: 404, Message: "gateway " + extID}
	}
	g.Address, g.ASN = addr, asn
	f.gateways[extID] = g
	return nil
}

func (f *fakePC) DeleteGateway(_ context.Context, extID string) error {
	f.log("DeleteGateway %s", extID)
	delete(f.gateways, extID)
	return nil
}

func (f *fakePC) ListBGPSessions(context.Context) ([]nutanix.BGPSession, error) {
	f.log("ListBGPSessions")
	var out []nutanix.BGPSession
	for _, s := range f.sessions {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakePC) GetBGPSession(_ context.Context, extID string) (*nutanix.BGPSession, error) {
	f.log("GetBGPSession %s", extID)
	s, ok := f.sessions[extID]
	if !ok {
		return nil, &nutanix.APIError{Status: 404, Message: "session " + extID}
	}
	f.mu.Lock()
	if len(f.sessionErrs) > 0 {
		err := f.sessionErrs[0]
		f.sessionErrs = f.sessionErrs[1:]
		f.mu.Unlock()
		return nil, err
	}
	s.State = pop(&f.sessionStates)
	f.mu.Unlock()
	if s.State == "DOWN" {
		s.Message = f.sessionMsg
	}
	return &s, nil
}

func (f *fakePC) CreateBGPSession(_ context.Context, name, projectExtID, localExtID, remoteExtID string, localAddr netip.Addr) (*nutanix.BGPSession, error) {
	f.log("CreateBGPSession %s project=%s local=%s remote=%s addr=%s", name, projectExtID, localExtID, remoteExtID, localAddr)
	if _, ok := f.gateways[remoteExtID]; !ok {
		return nil, &nutanix.APIError{Status: 404, Message: "remote gateway " + remoteExtID}
	}
	s := nutanix.BGPSession{ExtID: f.id("s"), Name: name, LocalExtID: localExtID, RemoteExtID: remoteExtID, AdvertiseAll: true}
	f.sessions[s.ExtID] = s
	return &s, nil
}

func (f *fakePC) DeleteBGPSession(_ context.Context, extID string) error {
	f.log("DeleteBGPSession %s", extID)
	delete(f.sessions, extID)
	return nil
}

func (f *fakePC) BGPRoutes(_ context.Context, sessionExtID string) ([]nutanix.BGPRoute, error) {
	f.log("BGPRoutes %s", sessionExtID)
	return f.routes, nil
}

func (f *fakePC) GetVM(_ context.Context, extID string) (*nutanix.VM, error) {
	f.log("GetVM %s", extID)
	v, ok := f.vms[extID]
	if !ok {
		return nil, &nutanix.APIError{Status: 404, Message: "vm " + extID}
	}
	return &v, nil
}

func (f *fakePC) PowerVM(_ context.Context, extID string, on bool) error {
	f.log("PowerVM %s on=%v", extID, on)
	v, ok := f.vms[extID]
	if !ok {
		return &nutanix.APIError{Status: 404, Message: "vm " + extID}
	}
	v.PowerOn = on
	f.vms[extID] = v
	return nil
}

func (f *fakePC) DeleteVM(_ context.Context, extID string) error {
	f.log("DeleteVM %s", extID)
	if v, ok := f.vms[extID]; ok && v.PowerOn {
		return errors.New("prism central: 409 the VM is powered on")
	}
	delete(f.vms, extID)
	return nil
}

func (f *fakePC) GetVPC(_ context.Context, extID string) (*nutanix.VPC, error) {
	f.log("GetVPC %s", extID)
	if extID != f.vpc.ExtID {
		return nil, &nutanix.APIError{Status: 404, Message: "vpc " + extID}
	}
	v := f.vpc
	return &v, nil
}

func (f *fakePC) GetSubnet(_ context.Context, extID string) (*nutanix.Subnet, error) {
	f.log("GetSubnet %s", extID)
	s, ok := f.subnets[extID]
	if !ok {
		return nil, &nutanix.APIError{Status: 404, Message: "subnet " + extID}
	}
	return &s, nil
}

func (f *fakePC) ListSubnets(context.Context) ([]nutanix.Subnet, error) {
	f.log("ListSubnets")
	var out []nutanix.Subnet
	for _, s := range f.subnets {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakePC) CreateVLANSubnet(_ context.Context, name, clusterExtID string, vlanID int, vswitch string) (*nutanix.Subnet, error) {
	f.log("CreateVLANSubnet %s cluster=%s vlan=%d vswitch=%s", name, clusterExtID, vlanID, vswitch)
	s := nutanix.Subnet{ExtID: f.id("vl"), Name: name, Type: "VLAN", ClusterExtID: clusterExtID, VLANID: vlanID, VirtualSwitchExtID: vswitch}
	f.subnets[s.ExtID] = s
	return &s, nil
}

func (f *fakePC) DeleteSubnet(_ context.Context, extID string) error {
	f.log("DeleteSubnet %s", extID)
	delete(f.subnets, extID)
	return nil
}

func (f *fakePC) UpdateSubnetGateway(_ context.Context, extID string, gw netip.Addr) error {
	f.log("UpdateSubnetGateway %s gw=%s", extID, gw)
	s, ok := f.subnets[extID]
	if !ok {
		return &nutanix.APIError{Status: 404, Message: "subnet " + extID}
	}
	s.Gateway = gw
	f.subnets[extID] = s
	return nil
}

// fakeEdge builds edge VMs into the fake, each with a fresh extId.
type fakeEdge struct {
	f         *fakePC
	fail      error
	failPower error // the VM is created, its power-on fails
}

func (e *fakeEdge) Build(_ context.Context, site settings.Site, p settings.SitePeering) (string, error) {
	e.f.log("EdgeBuild %s addr=%s", site.Code, p.EdgeAddr)
	if e.fail != nil {
		return "", e.fail
	}
	v := nutanix.VM{ExtID: e.f.id("edge"), Name: "portico-s1-edge", ClusterExtID: "cl-1", PowerOn: e.failPower == nil, Categories: []string{"portico:edge"}}
	e.f.vms[v.ExtID] = v
	return v.ExtID, e.failPower
}

// index returns the position of the first call starting with prefix, -1 when none.
func (f *fakePC) index(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func (f *fakePC) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// before fails the test unless call a happened and came before call b.
func (f *fakePC) before(t *testing.T, a, b string) {
	t.Helper()
	ia, ib := f.index(a), f.index(b)
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("want %q before %q; calls:\n  %s", a, b, strings.Join(f.calls, "\n  "))
	}
}

func (f *fakePC) never(t *testing.T, prefix string) {
	t.Helper()
	if f.index(prefix) >= 0 {
		t.Fatalf("%q was called; calls:\n  %s", prefix, strings.Join(f.calls, "\n  "))
	}
}

func testSite() settings.Site {
	return settings.Site{ID: "s1", Code: "S1", ClusterExtID: "cl-1", Network: settings.SiteNetwork{TransitVPCExtID: "vpc-1"}}
}

func routerPeering() settings.SitePeering {
	return settings.SitePeering{Kind: settings.PeerRouter, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.254"), GatewayASN: 65010,
		RouterAddr: netip.MustParseAddr("10.0.210.254"), RouterASN: 65000}
}

func edgePeering() settings.SitePeering {
	return settings.SitePeering{Kind: settings.PeerEdge, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.254"), GatewayASN: 65010,
		EdgeAddr: netip.MustParseAddr("10.0.210.194"), EdgeASN: 65020}
}

// built is an edge peering as Build leaves it, with its objects in the fake.
func built(f *fakePC) settings.SitePeering {
	p := edgePeering()
	f.gateways["gw-L"] = nutanix.Gateway{ExtID: "gw-L", Name: "portico-s1-bgp", ProjectExtID: systemProject, Local: true, Address: p.GatewayAddr.Addr(), ASN: 65010}
	f.gateways["rgw-R"] = nutanix.Gateway{ExtID: "rgw-R", Name: "portico-s1-peer", ProjectExtID: systemProject, Address: p.EdgeAddr, ASN: 65020}
	f.sessions["s-S"] = nutanix.BGPSession{ExtID: "s-S", Name: "portico-s1-peering", LocalExtID: "gw-L", RemoteExtID: "rgw-R", AdvertiseAll: true}
	f.vms["edge-E"] = nutanix.VM{ExtID: "edge-E", Name: "portico-s1-edge", ClusterExtID: "cl-1", PowerOn: true, Categories: []string{"portico:edge"}}
	s := f.subnets[extSubnet]
	s.Gateway = p.EdgeAddr
	f.subnets[extSubnet] = s
	p.LocalGatewayExtID, p.RemoteGatewayExtID, p.SessionExtID, p.EdgeVMExtID, p.VLANSubnetExtID = "gw-L", "rgw-R", "s-S", "edge-E", mgmtSubnet
	p.State = settings.PeeringUp
	return p
}
