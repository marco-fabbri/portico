package tenanttest

import (
	"context"
	"net/netip"
	"slices"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// pool hands out an overlay external subnet's addresses: floating IPs
// from the bottom, a VPC's SNAT address from the top.
type pool struct {
	first, last netip.Addr
	used        map[netip.Addr]bool
}

func (q *pool) low() netip.Addr {
	for a := q.first; a.Compare(q.last) <= 0; a = a.Next() {
		if !q.used[a] {
			q.used[a] = true
			return a
		}
	}
	return netip.Addr{}
}

func (q *pool) high() netip.Addr {
	for a := q.last; a.Compare(q.first) >= 0; a = a.Prev() {
		if !q.used[a] {
			q.used[a] = true
			return a
		}
	}
	return netip.Addr{}
}

// dropped reports whether the create of name keeps no project; the
// caller holds the lock.
func (p *PC) dropped(name string) bool {
	if p.DropProject[name] > 0 {
		p.DropProject[name]--
		return true
	}
	return false
}

// SubnetNamed returns the subnet of that name, or nil.
func (p *PC) SubnetNamed(name string) *nutanix.Subnet {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.Subnets {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

// VPCNamed returns the VPC of that name, or nil.
// HasSubnet reports whether a subnet with that extId exists.
func (p *PC) HasSubnet(extID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Subnets {
		if p.Subnets[i].ExtID == extID {
			return true
		}
	}
	return false
}

func (p *PC) VPCNamed(name string) *nutanix.VPC {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.VPCs {
		if v.Name == name {
			return &v
		}
	}
	return nil
}

// FloatingIPNamed returns the floating IP of that name, or nil.
func (p *PC) FloatingIPNamed(name string) *nutanix.FloatingIP {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.FloatingIPs {
		if f.Name == name {
			return &f
		}
	}
	return nil
}

// RoutesOf returns the routes of a VPC's route table.
func (p *PC) RoutesOf(vpcExtID string) []nutanix.Route {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.VPCRoutes[vpcExtID])
}

func (p *PC) ListSubnets(ctx context.Context) ([]nutanix.Subnet, error) {
	if err := p.call("ListSubnets"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.Subnets), nil
}

func (p *PC) GetSubnet(ctx context.Context, extID string) (*nutanix.Subnet, error) {
	if err := p.call("GetSubnet"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.Subnets {
		if s.ExtID == extID {
			s.SharedWith = slices.Clone(s.SharedWith)
			return &s, nil
		}
	}
	return nil, notFound("subnet " + extID)
}

func (p *PC) CreateOverlayExternalSubnet(ctx context.Context, name, vpcExtID string, pr netip.Prefix, gw, first, last netip.Addr) (*nutanix.Subnet, error) {
	if err := p.call("CreateOverlayExternalSubnet"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateOverlayExternalSubnet"]++
	s := nutanix.Subnet{ExtID: p.next("ext"), Name: name, Type: "OVERLAY", VPCExtID: vpcExtID, External: true, NAT: true, Prefix: pr}
	p.Subnets = append(p.Subnets, s)
	if p.pools == nil {
		p.pools = map[string]*pool{}
	}
	p.pools[s.ExtID] = &pool{first: first, last: last, used: map[netip.Addr]bool{}}
	return &s, nil
}

func (p *PC) CreateOverlaySubnet(ctx context.Context, name, projectExtID, vpcExtID string, pr netip.Prefix, gw, first, last netip.Addr) (*nutanix.Subnet, error) {
	if err := p.call("CreateOverlaySubnet"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateOverlaySubnet"]++
	s := nutanix.Subnet{ExtID: p.next("net"), Name: name, Type: "OVERLAY", ProjectExtID: projectExtID, VPCExtID: vpcExtID, Prefix: pr}
	if p.dropped(name) {
		s.ProjectExtID = ""
	}
	p.Subnets = append(p.Subnets, s)
	return &s, nil
}

func (p *PC) ShareSubnet(ctx context.Context, subnetExtID, projectExtID string) error {
	if err := p.call("ShareSubnet"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Subnets {
		if p.Subnets[i].ExtID == subnetExtID {
			if !slices.Contains(p.Subnets[i].SharedWith, projectExtID) {
				p.Subnets[i].SharedWith = append(p.Subnets[i].SharedWith, projectExtID)
			}
			return nil
		}
	}
	return notFound("subnet " + subnetExtID)
}

func (p *PC) UnshareSubnet(ctx context.Context, subnetExtID, projectExtID string) error {
	if err := p.call("UnshareSubnet"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Subnets {
		if p.Subnets[i].ExtID == subnetExtID {
			p.Subnets[i].SharedWith = slices.DeleteFunc(p.Subnets[i].SharedWith, func(s string) bool { return s == projectExtID })
			return nil
		}
	}
	return notFound("subnet " + subnetExtID)
}

func (p *PC) DeleteSubnet(ctx context.Context, extID string) error {
	if err := p.call("DeleteSubnet"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Subnets {
		if p.Subnets[i].ExtID == extID {
			p.Subnets = slices.Delete(p.Subnets, i, i+1)
			delete(p.pools, extID)
			return nil
		}
	}
	return notFound("subnet " + extID)
}

func (p *PC) ListVPCs(ctx context.Context) ([]nutanix.VPC, error) {
	if err := p.call("ListVPCs"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.VPCs), nil
}

func (p *PC) GetVPC(ctx context.Context, extID string) (*nutanix.VPC, error) {
	if err := p.call("GetVPC"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.VPCs {
		if v.ExtID == extID {
			return &v, nil
		}
	}
	return nil, notFound("vpc " + extID)
}

func (p *PC) CreateVPC(ctx context.Context, name, projectExtID, externalSubnetExtID string, dns []netip.Addr) (*nutanix.VPC, error) {
	if err := p.call("CreateVPC"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateVPC"]++
	v := nutanix.VPC{ExtID: p.next("vpc"), Name: name, Type: "REGULAR", ProjectExtID: projectExtID}
	if p.dropped(name) {
		v.ProjectExtID = ""
	}
	ext := nutanix.VPCExternal{SubnetExtID: externalSubnetExtID}
	if q := p.pools[externalSubnetExtID]; q != nil {
		ext.IPs = []netip.Addr{q.high()}
	}
	v.External = []nutanix.VPCExternal{ext}
	p.VPCs = append(p.VPCs, v)
	return &v, nil
}

func (p *PC) DeleteVPC(ctx context.Context, extID string) error {
	if err := p.call("DeleteVPC"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.VPCs {
		if p.VPCs[i].ExtID == extID {
			for _, ext := range p.VPCs[i].External {
				if q := p.pools[ext.SubnetExtID]; q != nil {
					for _, a := range ext.IPs {
						delete(q.used, a)
					}
				}
			}
			p.VPCs = slices.Delete(p.VPCs, i, i+1)
			delete(p.VPCRoutes, extID)
			return nil
		}
	}
	return notFound("vpc " + extID)
}

func (p *PC) Routes(ctx context.Context, vpcExtID string) (string, []nutanix.Route, error) {
	if err := p.call("Routes"); err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return "rt-" + vpcExtID, slices.Clone(p.VPCRoutes[vpcExtID]), nil
}

func (p *PC) CreateDefaultRoute(ctx context.Context, vpcExtID, routeTableExtID, externalSubnetExtID, projectExtID string) error {
	if err := p.call("CreateDefaultRoute"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateDefaultRoute"]++
	if p.VPCRoutes == nil {
		p.VPCRoutes = map[string][]nutanix.Route{}
	}
	p.VPCRoutes[vpcExtID] = append(p.VPCRoutes[vpcExtID], nutanix.Route{ExtID: p.next("route"), Destination: netip.MustParsePrefix("0.0.0.0/0"),
		NextHopType: "EXTERNAL_SUBNET", NextHopExtID: externalSubnetExtID})
	return nil
}

func (p *PC) ListFloatingIPs(ctx context.Context) ([]nutanix.FloatingIP, error) {
	if err := p.call("ListFloatingIPs"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.FloatingIPs), nil
}

func (p *PC) GetFloatingIP(ctx context.Context, extID string) (*nutanix.FloatingIP, error) {
	if err := p.call("GetFloatingIP"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.FloatingIPs {
		if f.ExtID == extID {
			return &f, nil
		}
	}
	return nil, notFound("floating ip " + extID)
}

func (p *PC) CreateFloatingIP(ctx context.Context, name, projectExtID, externalSubnetExtID string) (*nutanix.FloatingIP, error) {
	if err := p.call("CreateFloatingIP"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateFloatingIP"]++
	f := nutanix.FloatingIP{ExtID: p.next("fip"), Name: name, ProjectExtID: projectExtID, ExternalSubnetExtID: externalSubnetExtID}
	if p.dropped(name) {
		f.ProjectExtID = ""
	}
	if q := p.pools[externalSubnetExtID]; q != nil {
		f.IP = q.low()
	}
	p.FloatingIPs = append(p.FloatingIPs, f)
	return &f, nil
}

func (p *PC) DeleteFloatingIP(ctx context.Context, extID string) error {
	if err := p.call("DeleteFloatingIP"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, f := range p.FloatingIPs {
		if f.ExtID == extID {
			if q := p.pools[f.ExternalSubnetExtID]; q != nil {
				delete(q.used, f.IP)
			}
			p.FloatingIPs = slices.Delete(p.FloatingIPs, i, i+1)
			return nil
		}
	}
	return notFound("floating ip " + extID)
}
