package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// The site's peer: how the routed prefix reaches the site's network
// (prefix routing design 4.2). A site built before 0.3.0 has no kind
// and reads as static.
const (
	PeerStatic = "static" // a static route on the site's router, Portico builds nothing
	PeerRouter = "router" // the MSP's router peers by eBGP with the BGP gateway
	PeerEdge   = "edge"   // the edge router Portico builds peers, the site keeps one static route to it
)

// The peering's state (design 4.6), as the tenant placement's net_state.
const (
	PeeringNone     = "none"
	PeeringBuilding = "building"
	PeeringUp       = "up"
	PeeringDown     = "down"
	PeeringFailed   = "failed"
)

// SitePeering is the Routing section of the site's Networking card and
// what Portico built from it (design 4.6). Addresses are on the external
// subnet's VLAN, outside its pool; the ASNs are private.
type SitePeering struct {
	Kind        string       `json:"kind,omitempty"`
	GatewayAddr netip.Prefix `json:"gateway_addr,omitempty"`
	GatewayGW   netip.Addr   `json:"gateway_gw,omitempty"`
	GatewayASN  int          `json:"gateway_asn,omitempty"`
	RouterAddr  netip.Addr   `json:"router_addr,omitempty"`
	RouterASN   int          `json:"router_asn,omitempty"`
	EdgeAddr    netip.Addr   `json:"edge_addr,omitempty"`
	EdgeASN     int          `json:"edge_asn,omitempty"`
	// EdgeKept: after a move to the router the edge VM stays, powered off,
	// for the way back (design 4.5).
	EdgeKept bool `json:"edge_kept,omitempty"`
	// RouteAdded: the MSP confirmed the static route to the edge on the
	// site's network (edge mode) or the session on the router (router mode).
	RouteAdded bool `json:"route_added,omitempty"`
	// What Portico built in the site, by extId (design 4.1).
	LocalGatewayExtID  string    `json:"local_gateway_ext_id,omitempty"`
	RemoteGatewayExtID string    `json:"remote_gateway_ext_id,omitempty"`
	SessionExtID       string    `json:"session_ext_id,omitempty"`
	EdgeVMExtID        string    `json:"edge_vm_ext_id,omitempty"`
	EdgeImageExtID     string    `json:"edge_image_ext_id,omitempty"`
	VLANSubnetExtID    string    `json:"vlan_subnet_ext_id,omitempty"`
	State              string    `json:"state,omitempty"`
	Error              string    `json:"error,omitempty"`
	StateAt            time.Time `json:"state_at,omitempty"`
}

// KindOrStatic is the kind, static when unset (a 0.2.x site).
func (p SitePeering) KindOrStatic() string {
	if p.Kind == "" {
		return PeerStatic
	}
	return p.Kind
}

// privateASN reports whether asn is in a private range (RFC 6996):
// 64512-65534 or 4200000000-4294967294.
func privateASN(asn int) bool {
	return (asn >= 64512 && asn <= 65534) || (asn >= 4200000000 && asn <= 4294967294)
}

// Validate checks the section against the external subnet (its prefix
// and its pool): the gateway's address, and the router's or the edge's,
// are on the VLAN, outside the pool and distinct; each ASN is set and
// the two differ; Portico's own ASNs are private. Static needs nothing.
func (p SitePeering) Validate(external netip.Prefix, pool [2]netip.Addr) error {
	kind := p.KindOrStatic()
	switch kind {
	case PeerStatic:
		return nil
	case PeerRouter, PeerEdge:
	default:
		return fmt.Errorf("routing: unknown peer kind %q", p.Kind)
	}
	var errs []error
	onVLAN := func(what string, a netip.Addr) {
		switch {
		case !a.IsValid():
			errs = append(errs, fmt.Errorf("routing: %s needs an address", what))
		case external.IsValid() && !external.Contains(a):
			errs = append(errs, fmt.Errorf("routing: %s %s is not on the external subnet %s", what, a, external))
		case inPool(a, pool):
			errs = append(errs, fmt.Errorf("routing: %s %s is in the external subnet's pool %s-%s", what, a, pool[0], pool[1]))
		}
	}
	onVLAN("the BGP gateway", p.GatewayAddr.Addr())
	if p.GatewayAddr.IsValid() && external.IsValid() && p.GatewayAddr.Bits() != external.Bits() {
		errs = append(errs, fmt.Errorf("routing: the BGP gateway's prefix length /%d differs from the external subnet's /%d", p.GatewayAddr.Bits(), external.Bits()))
	}
	if !p.GatewayGW.IsValid() {
		errs = append(errs, errors.New("routing: the BGP gateway needs its default gateway"))
	}
	if p.GatewayAddr.IsValid() && p.GatewayAddr.Addr() == p.GatewayGW {
		errs = append(errs, fmt.Errorf("routing: the BGP gateway %s is the VLAN router's address", p.GatewayGW))
	}
	switch {
	case p.GatewayASN == 0:
		errs = append(errs, errors.New("routing: the BGP gateway needs an ASN"))
	case !privateASN(p.GatewayASN):
		errs = append(errs, fmt.Errorf("routing: the BGP gateway's ASN %d is not private (64512-65534 or 4200000000-4294967294)", p.GatewayASN))
	}
	// The chosen peer is checked in full; the other one too when any of
	// its fields is filled in, since a later move builds from it.
	peer := func(name string, addr netip.Addr, asn int, edge bool) {
		onVLAN(name, addr)
		if addr.IsValid() && addr == p.GatewayAddr.Addr() {
			errs = append(errs, fmt.Errorf("routing: the BGP gateway and %s need distinct addresses", name))
		}
		if edge && addr.IsValid() && addr == p.GatewayGW {
			errs = append(errs, fmt.Errorf("routing: %s %s is the VLAN router's address", name, addr))
		}
		switch {
		case asn == 0:
			errs = append(errs, fmt.Errorf("routing: %s needs an ASN", name))
		case edge && !privateASN(asn):
			errs = append(errs, fmt.Errorf("routing: the edge router's ASN %d is not private (64512-65534 or 4200000000-4294967294)", asn))
		case asn == p.GatewayASN:
			errs = append(errs, fmt.Errorf("routing: the BGP gateway and %s need distinct ASNs", name))
		}
	}
	if kind == PeerRouter || p.RouterAddr.IsValid() || p.RouterASN != 0 {
		peer("the router", p.RouterAddr, p.RouterASN, false)
	}
	if kind == PeerEdge || p.EdgeAddr.IsValid() || p.EdgeASN != 0 {
		peer("the edge router", p.EdgeAddr, p.EdgeASN, true)
	}
	return errors.Join(errs...)
}

func inPool(a netip.Addr, pool [2]netip.Addr) bool {
	return pool[0].IsValid() && pool[1].IsValid() && a.Compare(pool[0]) >= 0 && a.Compare(pool[1]) <= 0
}

// SwitchAllowed reports whether the ready-network switch may be on:
// with a router or edge peer only while the BGP session is UP (design
// 4.6); static imposes nothing.
func (n SiteNetwork) SwitchAllowed() (bool, string) {
	if n.Peering.KindOrStatic() == PeerStatic || n.Peering.State == PeeringUp {
		return true, ""
	}
	return false, "the BGP session is not UP yet"
}
