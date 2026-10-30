package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
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

// ErrInvalidSubnet refuses a subnet exception: not a private /24, or one
// that overlaps another tenant's subnet.
var ErrInvalidSubnet = errors.New("tenant: invalid subnet")

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
			return fmt.Errorf("%w: %q is not a prefix", ErrInvalidSubnet, exception)
		}
		for _, o := range tenants {
			if q, err := netip.ParsePrefix(o.Subnet); err == nil && o.ID != t.ID && q.Overlaps(p) {
				return fmt.Errorf("%w: %s overlaps %s, the subnet of tenant %s", ErrInvalidSubnet, p, q, o.ID)
			}
		}
		if err := netplan.CheckException(p, taken); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSubnet, err)
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
// the stored one, else the lowest that neither another connection to the
// site nor a subnet already in the transit VPC holds, stored at once. A
// connection whose network failed keeps its /28, unless it has no
// external subnet yet and a transit subnet now overlaps it.
func (s *Service) transitSlice(ctx context.Context, pc PrismCentral, p *store.Placement, site Site, own string) (netip.Prefix, error) {
	inTransit, err := transitSubnets(ctx, pc, site.Network.TransitVPCExtID, own)
	if err != nil {
		return netip.Prefix{}, err
	}
	if p.TransitPrefix != "" {
		stored, err := netip.ParsePrefix(p.TransitPrefix)
		if err != nil || p.ExtSubnetExtID != "" || !overlapsAny(stored, inTransit) {
			return stored, err
		}
		p.TransitPrefix = ""
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
	taken := inTransit
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

// siteFits refuses a ready network the site cannot carry, before anything
// is created: a tenant subnet on the site's own networks (the routed
// prefix or the transit VPC's VLAN external subnet) would be unreachable
// (design 4.2), and the transit VPC serves its own cluster only (4.1).
func siteFits(ctx context.Context, pc PrismCentral, t *store.Tenant, p *store.Placement, site Site) error {
	transit, err := pc.GetVPC(ctx, site.Network.TransitVPCExtID)
	if err != nil {
		return fmt.Errorf("transit VPC: %w", err)
	}
	tenantNet, _ := netip.ParsePrefix(t.Subnet)
	clash := func(other netip.Prefix, what string) error {
		if tenantNet.IsValid() && other.IsValid() && tenantNet.Overlaps(other) {
			return fmt.Errorf("the tenant subnet %s overlaps %s, %s: give the tenant another subnet", tenantNet, other.Masked(), what)
		}
		return nil
	}
	for _, r := range transit.RoutablePrefixes {
		if err := clash(r, "the routed prefix of "+transit.Name); err != nil {
			return err
		}
	}
	for _, x := range transit.External {
		ext, err := pc.GetSubnet(ctx, x.SubnetExtID)
		if err != nil {
			return fmt.Errorf("the transit VPC's external subnet: %w", err)
		}
		if err := clash(ext.Prefix, "the external subnet "+ext.Name); err != nil {
			return err
		}
		if ext.ClusterExtID != "" && p.ClusterExtID != "" && ext.ClusterExtID != p.ClusterExtID {
			return fmt.Errorf("the connection uses the cluster %s and the transit VPC the cluster %s: the ready network serves the transit VPC's cluster only", p.ClusterExtID, ext.ClusterExtID)
		}
	}
	return nil
}

// transitSubnets lists the prefixes of the subnets in the transit VPC but
// the tenant's own one (named own).
func transitSubnets(ctx context.Context, pc PrismCentral, transitExtID, own string) ([]netip.Prefix, error) {
	subs, err := pc.ListSubnets(ctx)
	if err != nil {
		return nil, fmt.Errorf("the transit VPC's subnets: %w", err)
	}
	var out []netip.Prefix
	for _, sub := range subs {
		if sub.VPCExtID == transitExtID && sub.Name != own && sub.Prefix.IsValid() {
			out = append(out, sub.Prefix.Masked())
		}
	}
	return out, nil
}

func overlapsAny(p netip.Prefix, others []netip.Prefix) bool {
	for _, o := range others {
		if p.Overlaps(o) {
			return true
		}
	}
	return false
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
			name := project + "-ext"
			if err := siteFits(ctx, pc, t, p, site); err != nil {
				return "", err
			}
			if slice, err = s.transitSlice(ctx, pc, p, site, name); err != nil {
				return "", err
			}
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
			_, detail, err := matchAllowance(ctx, pc, p, project, t.FIPAllowance)
			return detail, err
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

// matchAllowance brings the connection's floating IPs, <project>-fip-N,
// to n: the stored ones Prism Central still has, any of those names it
// made in a run cut off before they were stored, new ones with the
// lowest free names, and fewer by deleting unassociated ones from the
// highest name down (design 4.3). kept counts the associated ones it
// could not delete.
func matchAllowance(ctx context.Context, pc PrismCentral, p *store.Placement, project string, n int) (kept int, detail string, err error) {
	all, err := pc.ListFloatingIPs(ctx)
	if err != nil {
		return 0, "", err
	}
	stored := map[string]bool{}
	for _, f := range p.FloatingIPs {
		stored[f.ExtID] = true
	}
	index := func(name string) int {
		var i int
		if _, err := fmt.Sscanf(strings.TrimPrefix(name, project+"-fip-"), "%d", &i); err != nil || !strings.HasPrefix(name, project+"-fip-") {
			return 0
		}
		return i
	}
	var have []nutanix.FloatingIP
	for _, f := range all {
		if stored[f.ExtID] || (index(f.Name) > 0 && f.ProjectExtID == p.ProjectExtID) {
			have = append(have, f)
		}
	}
	sort.Slice(have, func(i, j int) bool { return index(have[i].Name) < index(have[j].Name) })
	save := func() {
		p.FloatingIPs = p.FloatingIPs[:0]
		for _, f := range have {
			p.FloatingIPs = append(p.FloatingIPs, store.FloatingIP{ExtID: f.ExtID, IP: addrString(f.IP)})
		}
	}
	defer save()
	created, deleted := 0, 0
	for next := 1; len(have) < n; next++ {
		name := fmt.Sprintf("%s-fip-%d", project, next)
		if slices.ContainsFunc(have, func(f nutanix.FloatingIP) bool { return f.Name == name }) {
			continue
		}
		made, err := inProject(ctx, name, p.ProjectExtID,
			func(ctx context.Context) (*nutanix.FloatingIP, error) {
				return pc.CreateFloatingIP(ctx, name, p.ProjectExtID, p.ExtSubnetExtID)
			},
			func(f *nutanix.FloatingIP) string { return f.ProjectExtID }, func(f *nutanix.FloatingIP) string { return f.ExtID }, pc.DeleteFloatingIP)
		if err != nil {
			return 0, "", err
		}
		have = append(have, *made)
		sort.Slice(have, func(i, j int) bool { return index(have[i].Name) < index(have[j].Name) })
		created++
	}
	for i := len(have) - 1; i >= 0 && len(have) > n; i-- {
		if have[i].Associated {
			continue
		}
		if err := pc.DeleteFloatingIP(ctx, have[i].ExtID); err != nil && !nutanix.IsNotFound(err) {
			return 0, "", err
		}
		have = slices.Delete(have, i, i+1)
		deleted++
	}
	kept = len(have) - n
	if kept < 0 {
		kept = 0
	}
	var ips []string
	for _, f := range have {
		ips = append(ips, addrString(f.IP))
	}
	detail = fmt.Sprintf("%d floating IP(s)", len(have))
	if len(ips) > 0 {
		detail += ": " + strings.Join(ips, ", ")
	}
	if created > 0 || deleted > 0 {
		detail += fmt.Sprintf(" (%d created, %d deleted)", created, deleted)
	}
	if kept > 0 {
		detail += fmt.Sprintf("; %d associated with a VM kept above the allowance of %d", kept, n)
	}
	return kept, detail, nil
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

// porticoObjects reads the ready network objects the placement stores
// and finds still in its project: their counts by content kind, the
// unassociated floating IPs only, and the floating IPs a VM uses, named
// "name (ip)".
func porticoObjects(ctx context.Context, pc PrismCentral, p *store.Placement) (map[string]int, []string, error) {
	if err := adoptByName(ctx, pc, p); err != nil {
		return nil, nil, err
	}
	counts := map[string]int{}
	if p.VPCExtID != "" {
		vpc, err := pc.GetVPC(ctx, p.VPCExtID)
		switch {
		case err == nil && vpc.ProjectExtID == p.ProjectExtID:
			counts["vpcs"]++
		case err != nil && !nutanix.IsNotFound(err):
			return nil, nil, err
		}
	}
	if p.SubnetExtID != "" {
		sub, err := pc.GetSubnet(ctx, p.SubnetExtID)
		switch {
		case err == nil && sub.ProjectExtID == p.ProjectExtID:
			counts["subnets"]++
		case err != nil && !nutanix.IsNotFound(err):
			return nil, nil, err
		}
	}
	var associated []string
	for _, f := range p.FloatingIPs {
		got, err := pc.GetFloatingIP(ctx, f.ExtID)
		switch {
		case nutanix.IsNotFound(err):
		case err != nil:
			return nil, nil, err
		case got.Associated:
			associated = append(associated, fmt.Sprintf("%s (%s)", got.Name, addrString(got.IP)))
		case got.ProjectExtID == p.ProjectExtID:
			counts["floating-ips"]++
		}
	}
	return counts, associated, nil
}

// adoptByName fills, on a placement that has or began a ready network,
// the extIds it lacks with the objects Portico names for its project:
// a create can succeed in Prism Central after its call failed, and what
// Disconnect does not find stays behind and overlaps the next tenant.
// The project's own objects must carry its project.
func adoptByName(ctx context.Context, pc PrismCentral, p *store.Placement) error {
	if !hasNetwork(p) || p.ProjectID == "" {
		return nil
	}
	project := p.ProjectID
	if p.ExtSubnetExtID == "" {
		sub, err := findSubnet(ctx, pc, "", project+"-ext")
		if err != nil {
			return err
		}
		if sub != nil && sub.External {
			p.ExtSubnetExtID = sub.ExtID
		}
	}
	if p.VPCExtID == "" {
		vpc, err := findVPC(ctx, pc, "", project+"-vpc")
		if err != nil {
			return err
		}
		if vpc != nil && vpc.ProjectExtID == p.ProjectExtID {
			p.VPCExtID = vpc.ExtID
		}
	}
	if p.SubnetExtID == "" {
		sub, err := findSubnet(ctx, pc, "", project+"-net")
		if err != nil {
			return err
		}
		if sub != nil && sub.ProjectExtID == p.ProjectExtID {
			p.SubnetExtID = sub.ExtID
		}
	}
	return nil
}

// networkRemoval is the ready network's part of Disconnect (design 4.4),
// in reverse: Portico's floating IPs (none associated), the subnet, the
// VPC with its route, the sharing and the external subnet, by stored
// extId; what is gone already is "already absent".
func (s *Service) networkRemoval(pc PrismCentral, p *store.Placement) []removalStep {
	absent := func(what string) (string, error) { return what + " already absent", nil }
	deleted := func(what string, err error) (string, error) {
		switch {
		case err == nil:
			return what + " deleted", nil
		case nutanix.IsNotFound(err):
			return absent(what)
		}
		return "", err
	}
	return []removalStep{
		{"net-floating-ips", func(ctx context.Context) (string, error) {
			if err := adoptByName(ctx, pc, p); err != nil {
				return "", err
			}
			n := 0
			for len(p.FloatingIPs) > 0 {
				f := p.FloatingIPs[0]
				got, err := pc.GetFloatingIP(ctx, f.ExtID)
				switch {
				case nutanix.IsNotFound(err):
				case err != nil:
					return "", err
				case got.Associated:
					return "", fmt.Errorf("floating IP %s (%s) is associated with a VM: Portico does not delete it", got.Name, addrString(got.IP))
				default:
					if err := pc.DeleteFloatingIP(ctx, f.ExtID); err != nil && !nutanix.IsNotFound(err) {
						return "", err
					}
					n++
				}
				p.FloatingIPs = p.FloatingIPs[1:]
			}
			p.FloatingIPs = nil
			if n == 0 {
				return absent("floating IPs")
			}
			return fmt.Sprintf("%d floating IP(s) deleted", n), nil
		}},
		{"net-subnet", func(ctx context.Context) (string, error) {
			if p.SubnetExtID == "" {
				return absent("subnet")
			}
			d, err := deleted("subnet", pc.DeleteSubnet(ctx, p.SubnetExtID))
			if err == nil {
				p.SubnetExtID = ""
			}
			return d, err
		}},
		{"net-vpc", func(ctx context.Context) (string, error) {
			if p.VPCExtID == "" {
				return absent("VPC")
			}
			d, err := deleted("VPC", pc.DeleteVPC(ctx, p.VPCExtID))
			if err == nil {
				p.VPCExtID, p.SNATIP = "", ""
			}
			return d, err
		}},
		{"net-unshare", func(ctx context.Context) (string, error) {
			if p.ExtSubnetExtID == "" || p.ProjectExtID == "" {
				return absent("external subnet sharing")
			}
			sub, err := pc.GetSubnet(ctx, p.ExtSubnetExtID)
			if nutanix.IsNotFound(err) {
				return absent("external subnet sharing")
			}
			if err != nil {
				return "", err
			}
			if !contains(sub.SharedWith, p.ProjectExtID) {
				return absent("external subnet sharing")
			}
			if err := pc.UnshareSubnet(ctx, p.ExtSubnetExtID, p.ProjectExtID); err != nil && !nutanix.IsNotFound(err) {
				return "", err
			}
			return "external subnet unshared", nil
		}},
		{"net-external-subnet", func(ctx context.Context) (string, error) {
			if p.ExtSubnetExtID == "" {
				return absent("external subnet")
			}
			d, err := deleted("external subnet", pc.DeleteSubnet(ctx, p.ExtSubnetExtID))
			if err == nil {
				p.ExtSubnetExtID, p.NetState, p.NetError = "", store.NetNone, ""
			}
			return d, err
		}},
	}
}

// hasNetwork reports whether the placement has, or had begun, a ready
// network.
func hasNetwork(p *store.Placement) bool {
	return p.NetState != store.NetNone || p.ExtSubnetExtID != "" || p.VPCExtID != "" || p.SubnetExtID != "" || len(p.FloatingIPs) > 0
}

// ErrInvalidAllowance refuses a floating IP allowance outside 0 to
// netplan.MaxAllowance.
var ErrInvalidAllowance = fmt.Errorf("tenant: the floating IP allowance must be between 0 and %d", netplan.MaxAllowance)

// networkPlacement returns the tenant's connection to the site with its
// site, in a status a ready network can be built on: active or suspended.
func (s *Service) networkPlacement(ctx context.Context, id, siteID string) (*store.Tenant, *store.Placement, Site, error) {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return nil, nil, Site{}, err
	}
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return nil, nil, Site{}, err
	}
	site, err := s.site(siteID)
	if err != nil {
		return nil, nil, Site{}, fmt.Errorf("%w: %q", ErrUnknownSite, siteID)
	}
	if p.Status != store.PlacementActive && p.Status != store.PlacementSuspended {
		return nil, nil, Site{}, fmt.Errorf("%w: the connection to %s is %s", ErrState, site.Code, p.Status)
	}
	return t, p, site, nil
}

// RetryNetwork runs the ready network's steps again, alone, on a
// connection whose network failed (design 4.3, Retry network).
func (s *Service) RetryNetwork(ctx context.Context, id, siteID string) error {
	t, p, site, err := s.networkPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	if p.NetState != store.NetFailed {
		return fmt.Errorf("%w: the ready network on %s is %s, not failed", ErrState, site.Code, p.NetState)
	}
	return s.startNetwork(t, p, site)
}

// CreateNetwork builds the ready network of a connection that has none,
// on a site whose ready network is on (design 4.3, Create ready network).
func (s *Service) CreateNetwork(ctx context.Context, id, siteID string) error {
	t, p, site, err := s.networkPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	if p.NetState != store.NetNone {
		return fmt.Errorf("%w: the connection to %s already has a ready network (%s)", ErrState, site.Code, p.NetState)
	}
	if !site.Network.Enabled {
		return fmt.Errorf("%w: the ready network of %s is off", ErrState, site.Code)
	}
	return s.startNetwork(t, p, site)
}

// startNetwork runs buildNetwork in the background under the tenant's
// lock; it reads the rows again once it holds it.
func (s *Service) startNetwork(t *store.Tenant, p *store.Placement, site Site) error {
	if !s.acquire(t.ID) {
		return ErrBusy
	}
	s.changed(t.ID)
	s.background(t.ID, "network", func(ctx context.Context) error {
		defer s.changed(t.ID)
		t, err := s.st.GetTenant(ctx, t.ID)
		if err != nil {
			return err
		}
		p, err := s.st.GetPlacement(ctx, t.ID, p.SiteID)
		if err != nil {
			return err
		}
		return s.buildNetwork(ctx, t, p, site)
	})
	return nil
}

// SetAllowance stores the tenant's floating IP allowance and brings every
// ready network to it, under the tenant's lock. kept counts the floating
// IPs a VM uses that stay above the allowance.
func (s *Service) SetAllowance(ctx context.Context, id string, n int) (int, error) {
	if n < 0 || n > netplan.MaxAllowance {
		return 0, ErrInvalidAllowance
	}
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return 0, err
	}
	if !s.acquire(id) {
		return 0, ErrBusy
	}
	defer s.release(id)
	defer s.changed(id)
	t.FIPAllowance = n
	if err := s.st.UpdateTenant(ctx, t); err != nil {
		return 0, err
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		return 0, err
	}
	kept := 0
	var failures []string
	for i := range ps {
		p := &ps[i]
		if p.NetState != store.NetReady {
			continue
		}
		site, err := s.site(p.SiteID)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		err = s.record(ctx, id, p.SiteID, "net-floating-ips", func(ctx context.Context) (string, error) {
			k, detail, err := matchAllowance(ctx, site.PC, p, s.projectName(id), n)
			kept += k
			return detail, err
		})
		if uerr := s.st.UpdatePlacement(context.WithoutCancel(ctx), p); uerr != nil && err == nil {
			err = uerr
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("site %s: %v", site.Code, err))
		}
	}
	if len(failures) > 0 {
		return kept, errors.New(strings.Join(failures, "; "))
	}
	return kept, nil
}
