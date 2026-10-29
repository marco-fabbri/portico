package nutanix

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
)

// PathNetworking is the Flow Virtual Networking API a tenant's ready
// network uses (write-up 34: v4.0 to v4.4 answer on pc.7.6, v4.5 is 404).
const PathNetworking = "/api/networking/v4.4/config"

// Subnet is a subnet as Prism Central stores it. A subnet shared into a
// project carries its owner's projectExtId (an overlay external subnet in
// the transit VPC carries none).
type Subnet struct {
	ExtID, Name, Type, ProjectExtID, VPCExtID string
	External, NAT                             bool
	Prefix                                    netip.Prefix
	SharedWith                                []string
}

// VPC is a VPC with its external subnets and the addresses Prism Central
// gave it on each.
type VPC struct {
	ExtID, Name, Type, ProjectExtID string
	External                        []VPCExternal
	RoutablePrefixes                []netip.Prefix
}

// VPCExternal is one external subnet of a VPC.
type VPCExternal struct {
	SubnetExtID string
	IPs         []netip.Addr
}

// FloatingIP is a floating IP; Associated is true when Prism Central says
// it is associated with a NIC or a private IP.
type FloatingIP struct {
	ExtID, Name, ProjectExtID, ExternalSubnetExtID string
	IP                                             netip.Addr
	AssociatedVMNIC                                string
	Associated                                     bool
}

// Route is a route of a VPC's route table.
type Route struct {
	ExtID                     string
	Destination               netip.Prefix
	NextHopType, NextHopExtID string
}

type ipValue struct {
	Value string `json:"value"`
}

type ipv4Prefix struct {
	IPv4 struct {
		IP           ipValue `json:"ip"`
		PrefixLength int     `json:"prefixLength"`
	} `json:"ipv4"`
}

func (p ipv4Prefix) prefix() netip.Prefix {
	a, err := netip.ParseAddr(p.IPv4.IP.Value)
	if err != nil {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(a, p.IPv4.PrefixLength)
}

func parseAddr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

type rawSubnet struct {
	ExtID        string `json:"extId"`
	Name         string `json:"name"`
	SubnetType   string `json:"subnetType"`
	ProjectExtID string `json:"projectExtId"`
	VPCReference string `json:"vpcReference"`
	IsExternal   bool   `json:"isExternal"`
	IsNAT        bool   `json:"isNatEnabled"`
	IPConfig     []struct {
		IPv4 struct {
			IPSubnet struct {
				IP           ipValue `json:"ip"`
				PrefixLength int     `json:"prefixLength"`
			} `json:"ipSubnet"`
		} `json:"ipv4"`
	} `json:"ipConfig"`
	SharedWithProjects []string `json:"sharedWithProjects"`
}

func (r rawSubnet) subnet() Subnet {
	s := Subnet{ExtID: r.ExtID, Name: r.Name, Type: r.SubnetType, ProjectExtID: r.ProjectExtID, VPCExtID: r.VPCReference,
		External: r.IsExternal, NAT: r.IsNAT, SharedWith: r.SharedWithProjects}
	if len(r.IPConfig) > 0 {
		if a, err := netip.ParseAddr(r.IPConfig[0].IPv4.IPSubnet.IP.Value); err == nil {
			s.Prefix = netip.PrefixFrom(a, r.IPConfig[0].IPv4.IPSubnet.PrefixLength)
		}
	}
	return s
}

type rawVPC struct {
	ExtID           string `json:"extId"`
	Name            string `json:"name"`
	VPCType         string `json:"vpcType"`
	ProjectExtID    string `json:"projectExtId"`
	ExternalSubnets []struct {
		SubnetReference string `json:"subnetReference"`
		ExternalIPs     []struct {
			IPv4 ipValue `json:"ipv4"`
		} `json:"externalIps"`
	} `json:"externalSubnets"`
	ExternallyRoutablePrefixes []ipv4Prefix `json:"externallyRoutablePrefixes"`
}

func (r rawVPC) vpc() VPC {
	v := VPC{ExtID: r.ExtID, Name: r.Name, Type: r.VPCType, ProjectExtID: r.ProjectExtID}
	for _, e := range r.ExternalSubnets {
		ve := VPCExternal{SubnetExtID: e.SubnetReference}
		for _, ip := range e.ExternalIPs {
			if a := parseAddr(ip.IPv4.Value); a.IsValid() {
				ve.IPs = append(ve.IPs, a)
			}
		}
		v.External = append(v.External, ve)
	}
	for _, p := range r.ExternallyRoutablePrefixes {
		if pp := p.prefix(); pp.IsValid() {
			v.RoutablePrefixes = append(v.RoutablePrefixes, pp)
		}
	}
	return v
}

type rawFloatingIP struct {
	ExtID                   string `json:"extId"`
	Name                    string `json:"name"`
	ProjectExtID            string `json:"projectExtId"`
	ExternalSubnetReference string `json:"externalSubnetReference"`
	FloatingIP              struct {
		IPv4 ipValue `json:"ipv4"`
	} `json:"floatingIp"`
	AssociationStatus string `json:"associationStatus"`
	Association       struct {
		VMNICReference string `json:"vmNicReference"`
	} `json:"association"`
}

func (r rawFloatingIP) floatingIP() FloatingIP {
	return FloatingIP{ExtID: r.ExtID, Name: r.Name, ProjectExtID: r.ProjectExtID, ExternalSubnetExtID: r.ExternalSubnetReference,
		IP: parseAddr(r.FloatingIP.IPv4.Value), AssociatedVMNIC: r.Association.VMNICReference,
		Associated: r.AssociationStatus != "" && r.AssociationStatus != "NOT_ASSOCIATED"}
}

// ListSubnets returns every subnet the caller can see.
func (c *Client) ListSubnets(ctx context.Context) ([]Subnet, error) {
	var raw []rawSubnet
	if err := c.listAll(ctx, PathNetworking+"/subnets", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Subnet, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.subnet())
	}
	return out, nil
}

// GetSubnet reads one subnet.
func (c *Client) GetSubnet(ctx context.Context, extID string) (*Subnet, error) {
	var r rawSubnet
	if _, err := c.do(ctx, http.MethodGet, PathNetworking+"/subnets/"+extID, "", nil, "", &r); err != nil {
		return nil, err
	}
	s := r.subnet()
	return &s, nil
}

func ipConfig(p netip.Prefix, gw, first, last netip.Addr) []map[string]any {
	return []map[string]any{{"ipv4": map[string]any{
		"ipSubnet":         map[string]any{"ip": map[string]string{"value": p.Addr().String()}, "prefixLength": p.Bits()},
		"defaultGatewayIp": map[string]string{"value": gw.String()},
		"poolList":         []map[string]any{{"startIp": map[string]string{"value": first.String()}, "endIp": map[string]string{"value": last.String()}}},
	}}}
}

// createWithTask posts body, waits for its task, then returns the stored
// entity of that name through find.
func createWithTask[T any](ctx context.Context, c *Client, path, name string, body any, find func(context.Context) ([]T, error), nameOf func(T) string) (*T, error) {
	var task taskRef
	if _, err := c.do(ctx, http.MethodPost, path, "", body, "", &task); err != nil {
		return nil, err
	}
	if err := c.WaitTask(ctx, task.ExtID); err != nil {
		return nil, err
	}
	all, err := find(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if nameOf(all[i]) == name {
			return &all[i], nil
		}
	}
	return nil, notFound(name)
}

func subnetName(s Subnet) string  { return s.Name }
func vpcName(v VPC) string        { return v.Name }
func fipName(f FloatingIP) string { return f.Name }

// CreateOverlayExternalSubnet creates a tenant's NAT overlay external
// subnet in the transit VPC vpcExtID; it belongs to no project (design
// 4.3 step 1).
func (c *Client) CreateOverlayExternalSubnet(ctx context.Context, name, vpcExtID string, p netip.Prefix, gw, first, last netip.Addr) (*Subnet, error) {
	body := map[string]any{"name": name, "subnetType": "OVERLAY", "vpcReference": vpcExtID, "isExternal": true, "isNatEnabled": true,
		"ipConfig": ipConfig(p, gw, first, last)}
	return createWithTask(ctx, c, PathNetworking+"/subnets", name, body, c.ListSubnets, subnetName)
}

// CreateOverlaySubnet creates a tenant's internal subnet in its VPC.
func (c *Client) CreateOverlaySubnet(ctx context.Context, name, projectExtID, vpcExtID string, p netip.Prefix, gw, first, last netip.Addr) (*Subnet, error) {
	body := map[string]any{"name": name, "subnetType": "OVERLAY", "projectExtId": projectExtID, "vpcReference": vpcExtID,
		"ipConfig": ipConfig(p, gw, first, last)}
	return createWithTask(ctx, c, PathNetworking+"/subnets", name, body, c.ListSubnets, subnetName)
}

// ShareSubnet shares a subnet with a project; Prism Central refuses a VPC
// whose external subnet is not shared with its project (NETWORKING-10056).
func (c *Client) ShareSubnet(ctx context.Context, subnetExtID, projectExtID string) error {
	return c.subnetAction(ctx, subnetExtID, "share", projectExtID)
}

// UnshareSubnet gives a subnet back; refused while any VPC uses it
// (NETWORKING-10068, write-up 34).
func (c *Client) UnshareSubnet(ctx context.Context, subnetExtID, projectExtID string) error {
	return c.subnetAction(ctx, subnetExtID, "unshare", projectExtID)
}

func (c *Client) subnetAction(ctx context.Context, subnetExtID, action, projectExtID string) error {
	path := PathNetworking + "/subnets/" + subnetExtID
	etag, err := c.etagOf(ctx, path)
	if err != nil {
		return err
	}
	var task taskRef
	if _, err := c.do(ctx, http.MethodPost, path+"/$actions/"+action, "", map[string]string{"projectExtId": projectExtID}, etag, &task); err != nil {
		return err
	}
	if task.ExtID == "" {
		return nil
	}
	return c.WaitTask(ctx, task.ExtID)
}

// DeleteSubnet deletes a subnet and waits for the task.
func (c *Client) DeleteSubnet(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathNetworking+"/subnets/"+extID)
}

// ListVPCs returns every VPC the caller can see.
func (c *Client) ListVPCs(ctx context.Context) ([]VPC, error) {
	var raw []rawVPC
	if err := c.listAll(ctx, PathNetworking+"/vpcs", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]VPC, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.vpc())
	}
	return out, nil
}

// GetVPC reads one VPC.
func (c *Client) GetVPC(ctx context.Context, extID string) (*VPC, error) {
	var r rawVPC
	if _, err := c.do(ctx, http.MethodGet, PathNetworking+"/vpcs/"+extID, "", nil, "", &r); err != nil {
		return nil, err
	}
	v := r.vpc()
	return &v, nil
}

// CreateVPC creates a tenant's VPC in its project, attached to one
// external subnet, with the DNS servers its subnets' DHCP gives out.
func (c *Client) CreateVPC(ctx context.Context, name, projectExtID, externalSubnetExtID string, dns []netip.Addr) (*VPC, error) {
	servers := make([]map[string]any, 0, len(dns))
	for _, d := range dns {
		servers = append(servers, map[string]any{"ipv4": map[string]string{"value": d.String()}})
	}
	body := map[string]any{"name": name, "vpcType": "REGULAR", "projectExtId": projectExtID,
		"externalSubnets":   []map[string]string{{"subnetReference": externalSubnetExtID}},
		"commonDhcpOptions": map[string]any{"domainNameServers": servers}}
	return createWithTask(ctx, c, PathNetworking+"/vpcs", name, body, c.ListVPCs, vpcName)
}

// DeleteVPC deletes a VPC (its routes go with it) and waits for the task.
func (c *Client) DeleteVPC(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathNetworking+"/vpcs/"+extID)
}

// Routes returns a VPC's route table and its routes. A tenant's role sees
// the table only through this filter (write-up 34).
func (c *Client) Routes(ctx context.Context, vpcExtID string) (string, []Route, error) {
	var tables []struct {
		ExtID        string `json:"extId"`
		VPCReference string `json:"vpcReference"`
	}
	q := url.Values{"$filter": {"vpcReference eq '" + vpcExtID + "'"}}
	if err := c.listAll(ctx, PathNetworking+"/route-tables", q, &tables); err != nil {
		return "", nil, err
	}
	rt := ""
	for _, t := range tables {
		if t.VPCReference == vpcExtID {
			rt = t.ExtID
			break
		}
	}
	if rt == "" {
		return "", nil, notFound("route table of VPC " + vpcExtID)
	}
	var raw []struct {
		ExtID       string     `json:"extId"`
		Destination ipv4Prefix `json:"destination"`
		Nexthop     struct {
			Type      string `json:"nexthopType"`
			Reference string `json:"nexthopReference"`
		} `json:"nexthop"`
	}
	if err := c.listAll(ctx, PathNetworking+"/route-tables/"+rt+"/routes", nil, &raw); err != nil {
		return "", nil, err
	}
	routes := make([]Route, 0, len(raw))
	for _, r := range raw {
		routes = append(routes, Route{ExtID: r.ExtID, Destination: r.Destination.prefix(), NextHopType: r.Nexthop.Type, NextHopExtID: r.Nexthop.Reference})
	}
	return rt, routes, nil
}

// CreateDefaultRoute adds 0.0.0.0/0 to the external subnet: a VPC created
// through the API has only its local routes (write-up 34).
func (c *Client) CreateDefaultRoute(ctx context.Context, vpcExtID, routeTableExtID, externalSubnetExtID, projectExtID string) error {
	body := map[string]any{"name": "default", "routeType": "STATIC", "vpcReference": vpcExtID, "routeTableReference": routeTableExtID,
		"destination": map[string]any{"ipv4": map[string]any{"ip": map[string]string{"value": "0.0.0.0"}, "prefixLength": 0}},
		"nexthop":     map[string]string{"nexthopType": "EXTERNAL_SUBNET", "nexthopReference": externalSubnetExtID}}
	if projectExtID != "" {
		body["projectExtId"] = projectExtID
	}
	var task taskRef
	if _, err := c.do(ctx, http.MethodPost, PathNetworking+"/route-tables/"+routeTableExtID+"/routes", "", body, "", &task); err != nil {
		return err
	}
	return c.WaitTask(ctx, task.ExtID)
}

// ListFloatingIPs returns every floating IP the caller can see.
func (c *Client) ListFloatingIPs(ctx context.Context) ([]FloatingIP, error) {
	var raw []rawFloatingIP
	if err := c.listAll(ctx, PathNetworking+"/floating-ips", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]FloatingIP, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.floatingIP())
	}
	return out, nil
}

// GetFloatingIP reads one floating IP.
func (c *Client) GetFloatingIP(ctx context.Context, extID string) (*FloatingIP, error) {
	var r rawFloatingIP
	if _, err := c.do(ctx, http.MethodGet, PathNetworking+"/floating-ips/"+extID, "", nil, "", &r); err != nil {
		return nil, err
	}
	f := r.floatingIP()
	return &f, nil
}

// CreateFloatingIP creates an unassociated floating IP in a project from
// an external subnet.
func (c *Client) CreateFloatingIP(ctx context.Context, name, projectExtID, externalSubnetExtID string) (*FloatingIP, error) {
	body := map[string]string{"name": name, "projectExtId": projectExtID, "externalSubnetReference": externalSubnetExtID}
	return createWithTask(ctx, c, PathNetworking+"/floating-ips", name, body, c.ListFloatingIPs, fipName)
}

// DeleteFloatingIP deletes a floating IP and waits for the task. Prism
// Central deletes an associated one without refusing (write-up 34): the
// caller checks the association first.
func (c *Client) DeleteFloatingIP(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathNetworking+"/floating-ips/"+extID)
}
