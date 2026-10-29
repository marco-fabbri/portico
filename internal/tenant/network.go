package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/marco-fabbri/portico/internal/netplan"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// DefaultAddressPool is where the tenants' /24s come from unless the MSP
// sets another pool (tenant networking design 4.2).
var DefaultAddressPool = netip.MustParsePrefix("10.100.0.0/16")

// DefaultAllowance is a new tenant's floating IPs per site (design 4.3).
const DefaultAllowance = 1

// SiteNetwork is a site's ready network: whether new connections get one,
// the transit VPC their external subnets go in (its externally routable
// prefix holds the tenants' /28s) and the DNS servers of their VPCs.
type SiteNetwork struct {
	Enabled         bool
	TransitVPCExtID string
	DNS             []netip.Addr
}

// SetAddressPool replaces the pool new tenants' /24s come from (the MSP
// changes it only while no tenant has a subnet, design 4.2).
func (s *Service) SetAddressPool(p netip.Prefix) {
	if !p.IsValid() {
		p = DefaultAddressPool
	}
	s.netMu.Lock()
	s.cfg.AddressPool = p
	s.netMu.Unlock()
}

// pickSubnet sets t's /24: the exception when one is asked, checked
// against the other tenants' subnets, else the lowest free /24 of the
// pool. The caller holds netMu until the row is stored.
func (s *Service) pickSubnet(ctx context.Context, t *store.Tenant, exception string) error {
	tenants, err := s.st.ListTenants(ctx)
	if err != nil {
		return err
	}
	var taken []netip.Prefix
	for _, o := range tenants {
		if p, err := netip.ParsePrefix(o.Subnet); err == nil && o.ID != t.ID {
			taken = append(taken, p)
		}
	}
	if exception != "" {
		p, err := netip.ParsePrefix(exception)
		if err != nil {
			return fmt.Errorf("the subnet %q is not a prefix", exception)
		}
		if err := netplan.CheckException(p, taken); err != nil {
			return err
		}
		t.Subnet, t.SubnetException = p.Masked().String(), true
		return nil
	}
	p, err := netplan.NextFree(s.cfg.AddressPool, 24, taken)
	if err != nil {
		return fmt.Errorf("tenant address pool %s: %w", s.cfg.AddressPool, err)
	}
	t.Subnet, t.SubnetException = p.String(), false
	return nil
}

// AssignSubnets gives every tenant without a subnet the next free /24 of
// the pool, in creation order (the upgrade to 0.2.0, design 4.2), and
// returns how many it assigned.
func (s *Service) AssignSubnets(ctx context.Context) (int, error) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	tenants, err := s.st.ListTenants(ctx)
	if err != nil {
		return 0, err
	}
	sort.SliceStable(tenants, func(i, j int) bool { return tenants[i].CreatedAt.Before(tenants[j].CreatedAt) })
	n := 0
	for i := range tenants {
		t := &tenants[i]
		if t.Subnet != "" {
			continue
		}
		if err := s.pickSubnet(ctx, t, ""); err != nil {
			return n, err
		}
		if err := s.st.UpdateTenant(ctx, t); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// transitSlice returns the connection's /28 of the site's routed prefix:
// the stored one, else the lowest that no connection to the site holds,
// stored at once. A connection whose network failed keeps its /28.
func (s *Service) transitSlice(ctx context.Context, pc PrismCentral, p *store.Placement, site Site) (netip.Prefix, error) {
	if p.TransitPrefix != "" {
		return netip.ParsePrefix(p.TransitPrefix)
	}
	transit, err := pc.GetVPC(ctx, site.Network.TransitVPCExtID)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("transit VPC: %w", err)
	}
	if len(transit.RoutablePrefixes) == 0 {
		return netip.Prefix{}, fmt.Errorf("the transit VPC %s has no externally routable prefix", transit.Name)
	}
	s.netMu.Lock()
	defer s.netMu.Unlock()
	others, err := s.st.ListPlacementsBySite(ctx, p.SiteID)
	if err != nil {
		return netip.Prefix{}, err
	}
	var taken []netip.Prefix
	for _, o := range others {
		if o.TenantID == p.TenantID {
			continue
		}
		if q, err := netip.ParsePrefix(o.TransitPrefix); err == nil {
			taken = append(taken, q)
		}
	}
	slice, err := netplan.NextFree(transit.RoutablePrefixes[0], netplan.TransitBits, taken)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("routed prefix %s: %w", transit.RoutablePrefixes[0], err)
	}
	p.TransitPrefix = slice.String()
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		return netip.Prefix{}, err
	}
	return slice, nil
}

// errNoProject is an object Prism Central keeps without the project the
// request carried (write-up 34).
func errNoProject(name string) error {
	return fmt.Errorf("Prism Central created %s without its project", name)
}

// inProject creates an object of a project and checks the project Prism
// Central kept: an object without it is deleted and created once more.
func inProject[T any](ctx context.Context, name, project string, create func(ctx context.Context) (*T, error), projectOf func(*T) string, extIDOf func(*T) string, del func(ctx context.Context, extID string) error) (*T, error) {
	for range 2 {
		obj, err := create(ctx)
		if err != nil {
			return nil, err
		}
		if projectOf(obj) == project {
			return obj, nil
		}
		if err := del(ctx, extIDOf(obj)); err != nil && !nutanix.IsNotFound(err) {
			return nil, fmt.Errorf("%w; deleting it failed: %v", errNoProject(name), err)
		}
	}
	return nil, errNoProject(name)
}

// buildNetwork runs the ready network's steps on an active connection
// (design 4.3): each finds its object by the stored extId first, by name
// second, so a run again adopts what a cut-off one created. A failure is
// the network's state; the connection stays as it is.
func (s *Service) buildNetwork(ctx context.Context, t *store.Tenant, p *store.Placement, site Site) error {
	pc, project := site.PC, s.projectName(t.ID)
	p.NetState, p.NetError = store.NetBuilding, ""
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		return err
	}
	var slice netip.Prefix
	steps := []struct {
		name string
		fn   func(ctx context.Context) (string, error)
	}{
		{"net-external-subnet", func(ctx context.Context) (string, error) {
			var err error
			if slice, err = s.transitSlice(ctx, pc, p, site); err != nil {
				return "", err
			}
			name := project + "-ext"
			sub, err := findSubnet(ctx, pc, p.ExtSubnetExtID, name)
			if err != nil {
				return "", err
			}
			if sub == nil {
				gw, first, last := netplan.TransitRange(slice)
				if sub, err = pc.CreateOverlayExternalSubnet(ctx, name, site.Network.TransitVPCExtID, slice, gw, first, last); err != nil {
					return "", err
				}
			}
			p.ExtSubnetExtID = sub.ExtID
			return fmt.Sprintf("%s %s (%s)", name, slice, sub.ExtID), nil
		}},
		{"net-share", func(ctx context.Context) (string, error) {
			sub, err := pc.GetSubnet(ctx, p.ExtSubnetExtID)
			if err != nil {
				return "", err
			}
			if contains(sub.SharedWith, p.ProjectExtID) {
				return "already shared with " + project, nil
			}
			if err := pc.ShareSubnet(ctx, p.ExtSubnetExtID, p.ProjectExtID); err != nil {
				return "", err
			}
			return "shared with " + project, nil
		}},
		{"net-vpc", func(ctx context.Context) (string, error) {
			name := project + "-vpc"
			vpc, err := findVPC(ctx, pc, p.VPCExtID, name)
			if err != nil {
				return "", err
			}
			if vpc == nil {
				vpc, err = inProject(ctx, name, p.ProjectExtID,
					func(ctx context.Context) (*nutanix.VPC, error) {
						return pc.CreateVPC(ctx, name, p.ProjectExtID, p.ExtSubnetExtID, site.Network.DNS)
					},
					func(v *nutanix.VPC) string { return v.ProjectExtID }, func(v *nutanix.VPC) string { return v.ExtID }, pc.DeleteVPC)
				if err != nil {
					return "", err
				}
			}
			p.VPCExtID = vpc.ExtID
			return vpc.ExtID, nil
		}},
		{"net-default-route", func(ctx context.Context) (string, error) {
			rt, routes, err := pc.Routes(ctx, p.VPCExtID)
			if err != nil {
				return "", err
			}
			for _, r := range routes {
				if r.Destination.Bits() == 0 {
					return "default route present", nil
				}
			}
			if err := pc.CreateDefaultRoute(ctx, p.VPCExtID, rt, p.ExtSubnetExtID, p.ProjectExtID); err != nil {
				return "", err
			}
			return "default route via " + project + "-ext", nil
		}},
		{"net-subnet", func(ctx context.Context) (string, error) {
			name := project + "-net"
			prefix, err := netip.ParsePrefix(t.Subnet)
			if err != nil {
				return "", fmt.Errorf("the tenant has no subnet assigned")
			}
			sub, err := findSubnet(ctx, pc, p.SubnetExtID, name)
			if err != nil {
				return "", err
			}
			if sub == nil {
				gw, first, last := netplan.TenantRange(prefix)
				sub, err = inProject(ctx, name, p.ProjectExtID,
					func(ctx context.Context) (*nutanix.Subnet, error) {
						return pc.CreateOverlaySubnet(ctx, name, p.ProjectExtID, p.VPCExtID, prefix, gw, first, last)
					},
					func(s *nutanix.Subnet) string { return s.ProjectExtID }, func(s *nutanix.Subnet) string { return s.ExtID }, pc.DeleteSubnet)
				if err != nil {
					return "", err
				}
			}
			p.SubnetExtID = sub.ExtID
			return fmt.Sprintf("%s %s (%s)", name, prefix, sub.ExtID), nil
		}},
		{"net-floating-ips", func(ctx context.Context) (string, error) {
			return s.floatingIPs(ctx, pc, t, p, project)
		}},
		{"net-read-back", func(ctx context.Context) (string, error) {
			return readBack(ctx, pc, p, project)
		}},
	}
	for _, st := range steps {
		err := s.record(ctx, t.ID, p.SiteID, st.name, st.fn)
		if uerr := s.st.UpdatePlacement(context.WithoutCancel(ctx), p); uerr != nil && err == nil {
			err = uerr
		}
		if err != nil {
			p.NetState, p.NetError = store.NetFailed, err.Error()
			if uerr := s.st.UpdatePlacement(context.WithoutCancel(ctx), p); uerr != nil {
				s.log.Warn("placement update failed", "tenant", t.ID, "site", p.SiteID, "err", uerr.Error())
			}
			return err
		}
	}
	p.NetState, p.NetError = store.NetReady, ""
	return s.st.UpdatePlacement(ctx, p)
}

// findSubnet returns the subnet by its stored extId, else by name, else nil.
func findSubnet(ctx context.Context, pc PrismCentral, extID, name string) (*nutanix.Subnet, error) {
	if extID != "" {
		sub, err := pc.GetSubnet(ctx, extID)
		if err == nil || !nutanix.IsNotFound(err) {
			return sub, err
		}
	}
	all, err := pc.ListSubnets(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// findVPC returns the VPC by its stored extId, else by name, else nil.
func findVPC(ctx context.Context, pc PrismCentral, extID, name string) (*nutanix.VPC, error) {
	if extID != "" {
		vpc, err := pc.GetVPC(ctx, extID)
		if err == nil || !nutanix.IsNotFound(err) {
			return vpc, err
		}
	}
	all, err := pc.ListVPCs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// floatingIPs makes the tenant's allowance of floating IPs,
// <project>-fip-1 to -N, from its external subnet, unassociated.
func (s *Service) floatingIPs(ctx context.Context, pc PrismCentral, t *store.Tenant, p *store.Placement, project string) (string, error) {
	all, err := pc.ListFloatingIPs(ctx)
	if err != nil {
		return "", err
	}
	byID, byName := map[string]nutanix.FloatingIP{}, map[string]nutanix.FloatingIP{}
	for _, f := range all {
		byID[f.ExtID], byName[f.Name] = f, f
	}
	var kept []store.FloatingIP
	var ips []string
	for i := 1; i <= t.FIPAllowance; i++ {
		name := fmt.Sprintf("%s-fip-%d", project, i)
		f, ok := nutanix.FloatingIP{}, false
		if i <= len(p.FloatingIPs) {
			f, ok = byID[p.FloatingIPs[i-1].ExtID]
		}
		if !ok {
			f, ok = byName[name]
		}
		if !ok {
			made, err := inProject(ctx, name, p.ProjectExtID,
				func(ctx context.Context) (*nutanix.FloatingIP, error) {
					return pc.CreateFloatingIP(ctx, name, p.ProjectExtID, p.ExtSubnetExtID)
				},
				func(f *nutanix.FloatingIP) string { return f.ProjectExtID }, func(f *nutanix.FloatingIP) string { return f.ExtID }, pc.DeleteFloatingIP)
			if err != nil {
				p.FloatingIPs = kept
				return "", err
			}
			f = *made
		}
		kept = append(kept, store.FloatingIP{ExtID: f.ExtID, IP: addrString(f.IP)})
		ips = append(ips, addrString(f.IP))
	}
	p.FloatingIPs = kept
	if len(ips) == 0 {
		return "no floating IP (allowance 0)", nil
	}
	return fmt.Sprintf("%d floating IP(s): %s", len(ips), strings.Join(ips, ", ")), nil
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// readBack stores the VPC's SNAT address and the floating IPs' addresses,
// and checks that the VPC and the subnet still name the project.
func readBack(ctx context.Context, pc PrismCentral, p *store.Placement, project string) (string, error) {
	vpc, err := pc.GetVPC(ctx, p.VPCExtID)
	if err != nil {
		return "", err
	}
	if vpc.ProjectExtID != p.ProjectExtID {
		return "", errNoProject(vpc.Name)
	}
	sub, err := pc.GetSubnet(ctx, p.SubnetExtID)
	if err != nil {
		return "", err
	}
	if sub.ProjectExtID != p.ProjectExtID {
		return "", errNoProject(sub.Name)
	}
	p.SNATIP = ""
	for _, ext := range vpc.External {
		if ext.SubnetExtID == p.ExtSubnetExtID && len(ext.IPs) > 0 {
			p.SNATIP = ext.IPs[0].String()
		}
	}
	if p.SNATIP == "" {
		return "", errors.New("Prism Central gave " + vpc.Name + " no address on " + project + "-ext")
	}
	var ips []string
	for i, f := range p.FloatingIPs {
		got, err := pc.GetFloatingIP(ctx, f.ExtID)
		if err != nil {
			return "", err
		}
		p.FloatingIPs[i].IP = addrString(got.IP)
		ips = append(ips, p.FloatingIPs[i].IP)
	}
	return fmt.Sprintf("SNAT %s, floating IPs %s", p.SNATIP, strings.Join(ips, ", ")), nil
}
