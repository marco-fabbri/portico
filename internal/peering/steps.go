package peering

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

// Build creates what the peering needs and is missing (design 4.1):
// the VLAN subnet for the NICs, the local gateway (waiting for UP), the
// edge router in edge mode, the remote gateway, the session. In edge
// mode it then waits for the session and moves the external subnet's
// gateway to the edge; in router mode it reads the state back, since
// the session comes up when the MSP configures the router.
func (s *Service) Build(ctx context.Context, site settings.Site, p settings.SitePeering, edge EdgeBuilder) (settings.SitePeering, error) {
	r := s.start(ctx, site, p, settings.PeeringBuilding)
	r.step("external", r.findExternal)
	r.step("vlan-subnet", r.vlanSubnet)
	r.step("gateway-local", r.localGateway)
	if r.p.KindOrStatic() == settings.PeerEdge {
		r.step("edge", func() error { return r.edgeVM(edge) })
	}
	r.step("gateway-remote", r.remoteGateway)
	r.step("session", r.session)
	if r.p.KindOrStatic() == settings.PeerEdge {
		if r.settle(true) {
			r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.EdgeAddr) })
		}
	} else if r.settle(false) {
		// The external subnet's gateway follows the VLAN router the MSP
		// gave (design 4.5): a previous edge, or a value corrected in the
		// card, is put right when the session is already up. The session
		// is not waited for here: the MSP configures the neighbour after
		// this build, and Repair runs the same steps.
		r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.GatewayGW) })
	}
	return r.finish()
}

// MoveToRouter makes the MSP's router the peer (design 4.5): the remote
// gateway becomes the router, the session is waited for through it, the
// external subnet's gateway goes back to the VLAN's router and the edge
// is powered off but kept. A session that stays down leaves the edge
// running and the subnet's gateway on it: the card offers the way back.
func (s *Service) MoveToRouter(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	r := s.start(ctx, site, p, settings.PeeringBuilding)
	r.step("external", r.findExternal)
	// The stored kind follows the remote gateway: until Prism Central
	// peers with the router, the edge is still the peer and a failure
	// here leaves it so (Delete the edge router would otherwise remove
	// the live peer).
	r.step("gateway-remote", func() error {
		if err := r.setRemote(r.p.RouterAddr, r.p.RouterASN); err != nil {
			return err
		}
		r.p.Kind, r.p.RouteAdded, r.p.EdgeKept = settings.PeerRouter, false, r.p.EdgeVMExtID != ""
		return nil
	})
	if r.settle(true) {
		r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.GatewayGW) })
		r.step("edge-off", func() error { return r.edgePower(false) })
	}
	return r.finish()
}

// MoveToEdge makes the edge router the peer (design 4.5): the edge is
// built (or powered on), the remote gateway becomes it, the session is
// waited for and the external subnet's gateway moves to the edge.
func (s *Service) MoveToEdge(ctx context.Context, site settings.Site, p settings.SitePeering, edge EdgeBuilder) (settings.SitePeering, error) {
	r := s.start(ctx, site, p, settings.PeeringBuilding)
	r.step("external", r.findExternal)
	r.step("edge", func() error { return r.edgeVM(edge) })
	// An edge built while the router is still the peer is a kept one
	// until the remote gateway points at it (see MoveToRouter).
	r.p.EdgeKept = r.p.EdgeVMExtID != ""
	r.step("gateway-remote", func() error {
		if err := r.setRemote(r.p.EdgeAddr, r.p.EdgeASN); err != nil {
			return err
		}
		r.p.Kind, r.p.RouteAdded, r.p.EdgeKept = settings.PeerEdge, false, false
		return nil
	})
	if r.settle(true) {
		r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.EdgeAddr) })
	}
	return r.finish()
}

// BackToEdge is the rollback of MoveToRouter: the kept edge is powered
// on, the remote gateway and the subnet's gateway go back to it.
func (s *Service) BackToEdge(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	if p.EdgeVMExtID == "" {
		return p, errors.New("peering: no edge router is kept; build one with Move to the edge router")
	}
	return s.MoveToEdge(ctx, site, p, nil)
}

// ReplaceEdge rebuilds the edge router, break-before-make with the one
// address (design 4.3): the old VM is powered off and deleted, the new
// one built from the current image, the session waited for. A new
// address also moves the remote gateway and the subnet's gateway, and
// resets the route tick: the MSP moves its static route.
func (s *Service) ReplaceEdge(ctx context.Context, site settings.Site, p settings.SitePeering, edge EdgeBuilder) (settings.SitePeering, error) {
	if p.KindOrStatic() != settings.PeerEdge {
		return p, errors.New("peering: the edge router is not the peer")
	}
	r := s.start(ctx, site, p, settings.PeeringBuilding)
	r.step("external", r.findExternal)
	r.step("edge-delete", r.edgeDelete)
	r.step("edge", func() error { return r.edgeVM(edge) })
	r.step("gateway-remote", func() error {
		g, err := r.s.pc.GetGateway(r.ctx, r.p.RemoteGatewayExtID)
		if err != nil && !nutanix.IsNotFound(err) {
			return err
		}
		if err == nil && g.Address == r.p.EdgeAddr && g.ASN == r.p.EdgeASN {
			return nil
		}
		r.p.RouteAdded = false
		return r.setRemote(r.p.EdgeAddr, r.p.EdgeASN)
	})
	if r.settle(true) {
		r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.EdgeAddr) })
	}
	return r.finish()
}

// DeleteEdge removes the edge router kept after a move to the router.
func (s *Service) DeleteEdge(ctx context.Context, p settings.SitePeering) (settings.SitePeering, error) {
	if p.KindOrStatic() == settings.PeerEdge {
		return p, errors.New("peering: the edge router is the peer; move to your router first")
	}
	r := s.start(ctx, settings.Site{}, p, p.State)
	r.step("edge-delete", r.edgeDelete)
	r.p.EdgeKept = false
	return r.finish()
}

// TearDown deletes what Portico built (design 4.6): the session, the
// gateways, the edge VM, the VLAN subnet when Portico created it, and
// puts the external subnet's gateway back to the VLAN's router. The
// edge image stays. An object already gone is fine.
func (s *Service) TearDown(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	r := s.start(ctx, site, p, settings.PeeringBuilding)
	r.step("session", func() error {
		if r.p.SessionExtID == "" {
			return nil
		}
		if err := r.s.pc.DeleteBGPSession(r.ctx, r.p.SessionExtID); err != nil && !nutanix.IsNotFound(err) {
			return err
		}
		r.p.SessionExtID = ""
		r.store()
		return nil
	})
	for _, g := range []struct {
		name string
		id   *string
	}{{"gateway-local", &r.p.LocalGatewayExtID}, {"gateway-remote", &r.p.RemoteGatewayExtID}} {
		r.step(g.name, func() error {
			if *g.id == "" {
				return nil
			}
			if err := r.s.pc.DeleteGateway(r.ctx, *g.id); err != nil && !nutanix.IsNotFound(err) {
				return err
			}
			*g.id = ""
			r.store()
			return nil
		})
	}
	r.step("edge-delete", r.edgeDelete)
	r.step("external", r.findExternal)
	r.step("subnet-gateway", func() error { return r.subnetGateway(r.p.GatewayGW) })
	r.step("vlan-subnet", func() error {
		if r.p.VLANSubnetExtID == "" {
			return nil
		}
		sub, err := r.s.pc.GetSubnet(r.ctx, r.p.VLANSubnetExtID)
		if err == nil && sub.Name == r.names.vlan {
			if err := r.s.pc.DeleteSubnet(r.ctx, sub.ExtID); err != nil && !nutanix.IsNotFound(err) {
				return err
			}
		} else if err != nil && !nutanix.IsNotFound(err) {
			return err
		}
		r.p.VLANSubnetExtID = ""
		return nil
	})
	if r.err == nil {
		r.p.State, r.p.Error, r.p.EdgeKept, r.p.RouteAdded = settings.PeeringNone, "", false, false
	}
	return r.finish()
}

// findExternal reads the transit VPC's routed (no-NAT) external subnet.
func (r *run) findExternal() error {
	vpc, err := r.s.pc.GetVPC(r.ctx, r.site.Network.TransitVPCExtID)
	if err != nil {
		return fmt.Errorf("the transit VPC: %w", err)
	}
	for _, e := range vpc.External {
		sub, err := r.s.pc.GetSubnet(r.ctx, e.SubnetExtID)
		if err != nil {
			return fmt.Errorf("the transit VPC's external subnet: %w", err)
		}
		if sub.External && !sub.NAT {
			r.ext = sub
			return nil
		}
	}
	return errors.New("the transit VPC has no routed (no-NAT) external subnet")
}

// vlanSubnet finds the unmanaged VLAN subnet of the external subnet's
// VLAN on the site's cluster, or creates portico-<site>-vlan.
func (r *run) vlanSubnet() error {
	if r.p.VLANSubnetExtID != "" {
		if _, err := r.s.pc.GetSubnet(r.ctx, r.p.VLANSubnetExtID); err == nil {
			return nil
		} else if !nutanix.IsNotFound(err) {
			return err
		}
	}
	cluster := r.site.ClusterExtID
	if cluster == "" {
		cluster = r.ext.ClusterExtID
	}
	subs, err := r.s.pc.ListSubnets(r.ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Type == "VLAN" && sub.ExtID != r.ext.ExtID && sub.VLANID == r.ext.VLANID && sub.ClusterExtID == cluster && !sub.External && !sub.Prefix.IsValid() {
			r.p.VLANSubnetExtID = sub.ExtID
			r.store()
			return nil
		}
	}
	sub, err := r.s.pc.CreateVLANSubnet(r.ctx, r.names.vlan, cluster, r.ext.VLANID, r.ext.VirtualSwitchExtID)
	if err != nil {
		return err
	}
	r.p.VLANSubnetExtID = sub.ExtID
	r.store()
	return nil
}

// localGateway reuses the local gateway by extId, adopts one by name in
// the system project, or creates it; then waits for it to be UP.
func (r *run) localGateway() error {
	if r.p.LocalGatewayExtID != "" {
		if _, err := r.s.pc.GetGateway(r.ctx, r.p.LocalGatewayExtID); err == nil {
			return r.s.waitGateway(r.ctx, r.p.LocalGatewayExtID)
		} else if !nutanix.IsNotFound(err) {
			return err
		}
		r.p.LocalGatewayExtID = ""
	}
	all, err := r.s.pc.ListGateways(r.ctx)
	if err != nil {
		return err
	}
	for _, g := range all {
		if g.Name != r.names.local || !g.Local {
			continue
		}
		if g.ProjectExtID != SystemProject && g.ProjectExtID != "" {
			return fmt.Errorf("the gateway %s belongs to another project (%s)", g.Name, g.ProjectExtID)
		}
		r.p.LocalGatewayExtID = g.ExtID
		r.store()
		return r.s.waitGateway(r.ctx, g.ExtID)
	}
	cluster := r.site.ClusterExtID
	if cluster == "" {
		cluster = r.ext.ClusterExtID
	}
	g, err := r.s.pc.CreateLocalBGPGateway(r.ctx, r.names.local, SystemProject, cluster, r.p.VLANSubnetExtID, r.p.GatewayAddr, r.p.GatewayGW, r.p.GatewayASN, r.site.Network.TransitVPCExtID)
	if err != nil {
		return err
	}
	r.p.LocalGatewayExtID = g.ExtID
	r.store()
	return r.s.waitGateway(r.ctx, g.ExtID)
}

// edgeVM reuses the edge VM Portico knows (powered on when off) or
// builds a new one.
func (r *run) edgeVM(edge EdgeBuilder) error {
	if r.p.EdgeVMExtID != "" {
		vm, err := r.s.pc.GetVM(r.ctx, r.p.EdgeVMExtID)
		switch {
		case err == nil:
			if !vm.PowerOn {
				return r.s.pc.PowerVM(r.ctx, vm.ExtID, true)
			}
			return nil
		case !nutanix.IsNotFound(err):
			return err
		}
		r.p.EdgeVMExtID = ""
	}
	if edge == nil {
		return errors.New("the edge router is gone; build it again from the card")
	}
	id, err := edge.Build(r.ctx, r.site, r.p)
	if err != nil {
		return err
	}
	r.p.EdgeVMExtID = id
	r.store()
	return nil
}

// edgePower turns the edge VM on or off when it is not already so.
func (r *run) edgePower(on bool) error {
	if r.p.EdgeVMExtID == "" {
		return nil
	}
	vm, err := r.s.pc.GetVM(r.ctx, r.p.EdgeVMExtID)
	if err != nil {
		if nutanix.IsNotFound(err) {
			return nil
		}
		return err
	}
	if vm.PowerOn == on {
		return nil
	}
	return r.s.pc.PowerVM(r.ctx, vm.ExtID, on)
}

// edgeDelete powers the edge VM off and deletes it.
func (r *run) edgeDelete() error {
	if r.p.EdgeVMExtID == "" {
		return nil
	}
	if err := r.edgePower(false); err != nil {
		return err
	}
	if err := r.s.pc.DeleteVM(r.ctx, r.p.EdgeVMExtID); err != nil && !nutanix.IsNotFound(err) {
		return err
	}
	r.p.EdgeVMExtID = ""
	r.store()
	return nil
}

// remoteGateway reuses the remote gateway by extId (updated when the
// peer changed), adopts one by name in the system project, or creates
// it for the peer.
func (r *run) remoteGateway() error {
	addr, asn := peer(r.p)
	if r.p.RemoteGatewayExtID != "" {
		g, err := r.s.pc.GetGateway(r.ctx, r.p.RemoteGatewayExtID)
		switch {
		case err == nil && g.Address == addr && g.ASN == asn:
			return nil
		case err == nil:
			return r.setRemote(addr, asn)
		case !nutanix.IsNotFound(err):
			return err
		}
		r.p.RemoteGatewayExtID = ""
	}
	all, err := r.s.pc.ListGateways(r.ctx)
	if err != nil {
		return err
	}
	for _, g := range all {
		if g.Local || (g.Name != r.names.remote && g.Name != r.names.remote+"-2") {
			continue
		}
		if g.ProjectExtID != SystemProject && g.ProjectExtID != "" {
			return fmt.Errorf("the gateway %s belongs to another project (%s)", g.Name, g.ProjectExtID)
		}
		r.p.RemoteGatewayExtID = g.ExtID
		r.store()
		if g.Address != addr || g.ASN != asn {
			return r.setRemote(addr, asn)
		}
		return nil
	}
	g, err := r.s.pc.CreateRemoteBGPGateway(r.ctx, r.names.remote, SystemProject, addr, asn)
	if err != nil {
		return err
	}
	r.p.RemoteGatewayExtID = g.ExtID
	r.store()
	return nil
}

// setRemote points the remote gateway at addr/asn: an update in place
// (write-up 38, 2) or, when Prism Central refuses it, a new remote
// gateway, the session recreated on it, the old one deleted: the old
// session goes only once the new remote exists (write-up 38, 1).
func (r *run) setRemote(addr netip.Addr, asn int) error {
	if r.p.RemoteGatewayExtID == "" {
		return r.remoteGateway()
	}
	err := r.s.pc.UpdateRemoteGateway(r.ctx, r.p.RemoteGatewayExtID, addr, asn)
	if err == nil {
		return nil
	}
	r.s.log.Warn("peering: remote gateway update refused, recreating", "site", r.site.ID, "err", err)
	old := r.p.RemoteGatewayExtID
	name := r.names.remote
	if g, err := r.s.pc.GetGateway(r.ctx, old); err == nil && g.Name == name {
		name += "-2"
	}
	g, err := r.s.pc.CreateRemoteBGPGateway(r.ctx, name, SystemProject, addr, asn)
	if err != nil {
		return err
	}
	r.p.RemoteGatewayExtID = g.ExtID
	r.store()
	if r.p.SessionExtID != "" {
		if err := r.s.pc.DeleteBGPSession(r.ctx, r.p.SessionExtID); err != nil && !nutanix.IsNotFound(err) {
			return err
		}
		r.p.SessionExtID = ""
		r.store()
	}
	if err := r.session(); err != nil {
		return err
	}
	if err := r.s.pc.DeleteGateway(r.ctx, old); err != nil && !nutanix.IsNotFound(err) {
		return err
	}
	return nil
}

// session reuses the session by extId when it joins the gateways,
// adopts one by name, or creates it.
func (r *run) session() error {
	if r.p.SessionExtID != "" {
		sess, err := r.s.pc.GetBGPSession(r.ctx, r.p.SessionExtID)
		switch {
		case err == nil && sess.LocalExtID == r.p.LocalGatewayExtID && sess.RemoteExtID == r.p.RemoteGatewayExtID:
			return nil
		case err == nil:
			if err := r.s.pc.DeleteBGPSession(r.ctx, sess.ExtID); err != nil && !nutanix.IsNotFound(err) {
				return err
			}
		case !nutanix.IsNotFound(err):
			return err
		}
		r.p.SessionExtID = ""
		r.store()
	}
	all, err := r.s.pc.ListBGPSessions(r.ctx)
	if err != nil {
		return err
	}
	for _, sess := range all {
		if sess.Name != r.names.session {
			continue
		}
		if sess.LocalExtID == r.p.LocalGatewayExtID && sess.RemoteExtID == r.p.RemoteGatewayExtID {
			r.p.SessionExtID = sess.ExtID
			r.store()
			return nil
		}
		if err := r.s.pc.DeleteBGPSession(r.ctx, sess.ExtID); err != nil && !nutanix.IsNotFound(err) {
			return err
		}
	}
	sess, err := r.s.pc.CreateBGPSession(r.ctx, r.names.session, SystemProject, r.p.LocalGatewayExtID, r.p.RemoteGatewayExtID, r.p.GatewayAddr.Addr())
	if err != nil {
		return err
	}
	r.p.SessionExtID = sess.ExtID
	r.store()
	return nil
}

// subnetGateway sets the external subnet's default gateway to gw when it
// is not already so.
func (r *run) subnetGateway(gw netip.Addr) error {
	if r.ext == nil || !gw.IsValid() || r.ext.Gateway == gw {
		return nil
	}
	if err := r.s.pc.UpdateSubnetGateway(r.ctx, r.ext.ExtID, gw); err != nil {
		return err
	}
	r.ext.Gateway = gw
	return nil
}
