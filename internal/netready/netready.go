// Package netready reads whether a site can offer the tenant ready network
// (tenant networking design 4.1): each check says what is missing and what
// the MSP does about it. It changes nothing in Prism Central.
package netready

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/marco-fabbri/portico/internal/netplan"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

// Check statuses.
const (
	OK      = "ok"
	Failed  = "failed"
	Warning = "warning"
	Todo    = "todo"
)

// Reader is the part of the Prism Central client the checks read.
type Reader interface {
	Controllers(ctx context.Context) ([]nutanix.Controller, error)
	PrismGateway(ctx context.Context) (netip.Addr, netip.Prefix, error)
	ListClusters(ctx context.Context) ([]nutanix.Cluster, error)
	ListVPCs(ctx context.Context) ([]nutanix.VPC, error)
	GetSubnet(ctx context.Context, extID string) (*nutanix.Subnet, error)
	Routes(ctx context.Context, vpcExtID string) (string, []nutanix.Route, error)
	MissingOperations(ctx context.Context, userExtID string, names []string) ([]string, error)
	FindRole(ctx context.Context, displayName string) (*nutanix.Role, error)
}

// Check is one row of the Networking card.
type Check struct {
	Name, Status, Message string
}

// Ready reports whether the ready network can be switched on: nothing
// failed and nothing left to do (a warning does not stop it).
func Ready(checks []Check) bool {
	for _, c := range checks {
		if c.Status == Failed || c.Status == Todo {
			return false
		}
	}
	return len(checks) > 0
}

// Choice is a transit VPC the MSP can pick.
type Choice struct {
	VPCExtID, Name string
	ExternalSubnet string // the name of its no-NAT VLAN external subnet
	Address        netip.Addr
	RoutedPrefix   netip.Prefix
	Slices         int // tenant /28s the routed prefix holds
}

func slices(p netip.Prefix) int {
	if !p.IsValid() || p.Bits() > netplan.TransitBits {
		return 0
	}
	return 1 << (netplan.TransitBits - p.Bits())
}

// TransitChoices lists the site's VPCs of type TRANSIT.
func TransitChoices(ctx context.Context, r Reader) ([]Choice, error) {
	vpcs, err := r.ListVPCs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Choice
	for _, v := range vpcs {
		if v.Type != "TRANSIT" {
			continue
		}
		c := Choice{VPCExtID: v.ExtID, Name: v.Name}
		if len(v.External) > 0 {
			if len(v.External[0].IPs) > 0 {
				c.Address = v.External[0].IPs[0]
			}
			if s, err := r.GetSubnet(ctx, v.External[0].SubnetExtID); err == nil {
				c.ExternalSubnet = s.Name
			}
		}
		if len(v.RoutablePrefixes) > 0 {
			c.RoutedPrefix = v.RoutablePrefixes[0]
			c.Slices = slices(c.RoutedPrefix)
		}
		out = append(out, c)
	}
	return out, nil
}

// Evaluate runs the checks in card order: flow, mode, gateway, transit,
// transit-valid, rights, pool. used is how many tenant /28s the site's
// connections hold; saExtID is the service account; tenantRole is the
// role the site setup creates for tenants, checked when the MSP keeps it
// ("" skips the check).
func Evaluate(ctx context.Context, r Reader, n settings.SiteNetwork, saExtID string, used int, tenantRole string) []Check {
	var out []Check
	add := func(name, status, msg string) { out = append(out, Check{Name: name, Status: status, Message: msg}) }

	ctrls, err := r.Controllers(ctx)
	up := false
	for _, c := range ctrls {
		up = up || c.Status == "UP"
	}
	switch {
	case err != nil:
		add("flow", Failed, "Cannot read the networking controller: "+err.Error())
	case !up:
		add("flow", Failed, "Enable Flow Virtual Networking in Prism Central (Integrated mode)")
	default:
		add("flow", OK, "Flow Virtual Networking controller up")
	}

	if n.IntegratedConfirmed {
		add("mode", OK, "Integrated mode, confirmed by the MSP")
	} else {
		add("mode", Todo, "Confirm that Flow Virtual Networking runs in the Integrated mode: Portico supports only that one")
	}

	add(gatewayCheck(ctx, r))

	var prefix netip.Prefix
	if n.TransitVPCExtID == "" {
		add("transit", Todo, "Pick the transit VPC tenants use")
		add("transit-valid", Todo, "No transit VPC picked")
	} else {
		add("transit", OK, "Transit VPC picked")
		msg, p := transitCheck(ctx, r, n.TransitVPCExtID)
		if msg != "" {
			add("transit-valid", Failed, msg)
		} else {
			prefix = p
			add("transit-valid", OK, "Transit VPC on a no-NAT external subnet, default route, routed prefix "+p.String())
		}
	}

	if saExtID == "" {
		add("rights", Failed, "No service account stored for this site: Repair the site")
	} else if missing, err := r.MissingOperations(ctx, saExtID, settings.NetworkingOperations); err != nil {
		add("rights", Failed, "Cannot read the service account's rights: "+err.Error())
	} else if len(missing) > 0 {
		add("rights", Failed, "The service account lacks "+strings.Join(missing, ", ")+": Repair the site")
	} else {
		add("rights", OK, "The service account has the networking operations")
	}

	if tenantRole != "" {
		switch _, err := r.FindRole(ctx, tenantRole); {
		case nutanix.IsNotFound(err):
			add("tenant-role", Failed, "No "+tenantRole+" role: tenants would keep Project Admin, which creates floating IPs. Repair the site")
		case err != nil:
			add("tenant-role", Failed, "Cannot read the "+tenantRole+" role: "+err.Error())
		default:
			add("tenant-role", OK, "Tenants get the "+tenantRole+" role")
		}
	}

	if len(n.DNS) == 0 {
		add("dns", Todo, "Give the DNS servers the tenant subnets hand out")
	} else {
		add("dns", OK, "Tenant subnets hand out "+strings.Join(n.DNS, ", "))
	}

	if total := slices(prefix); total == 0 {
		add("pool", Todo, "No routed prefix to cut tenant ranges from")
	} else if used*5 >= total*4 {
		add("pool", Warning, fmt.Sprintf("%d of %d tenant ranges used", used, total))
	} else {
		add("pool", OK, fmt.Sprintf("%d of %d tenant ranges used", used, total))
	}
	return out
}

func gatewayCheck(ctx context.Context, r Reader) (string, string, string) {
	gw, own, err := r.PrismGateway(ctx)
	if err != nil {
		return "gateway", Failed, "Cannot read Prism Central's gateway: " + err.Error()
	}
	if !gw.IsValid() || !own.IsValid() || !own.Masked().Contains(gw) {
		return "gateway", Failed, fmt.Sprintf("Prism Central's gateway %s is outside its subnet %s", gw, own.Masked())
	}
	clusters, err := r.ListClusters(ctx)
	if err != nil {
		return "gateway", Failed, "Cannot read the clusters' addresses: " + err.Error()
	}
	for _, c := range clusters {
		for _, a := range c.Addresses {
			if a == gw {
				return "gateway", Failed, fmt.Sprintf("Prism Central's gateway is %s, an address of the cluster %s; Flow cannot download its packages", gw, c.Name)
			}
		}
	}
	return "gateway", OK, "Prism Central's gateway " + gw.String()
}

// transitCheck returns what is wrong with the picked transit VPC, or its
// routed prefix.
func transitCheck(ctx context.Context, r Reader, extID string) (string, netip.Prefix) {
	vpcs, err := r.ListVPCs(ctx)
	if err != nil {
		return "Cannot read the VPCs: " + err.Error(), netip.Prefix{}
	}
	var v *nutanix.VPC
	for i := range vpcs {
		if vpcs[i].ExtID == extID {
			v = &vpcs[i]
		}
	}
	switch {
	case v == nil:
		return "The picked transit VPC no longer exists in Prism Central", netip.Prefix{}
	case v.Type != "TRANSIT":
		return v.Name + " is not a transit VPC", netip.Prefix{}
	case len(v.External) == 0:
		return v.Name + " has no external subnet", netip.Prefix{}
	case len(v.RoutablePrefixes) == 0:
		return v.Name + " has no externally routable prefix to give tenants", netip.Prefix{}
	}
	s, err := r.GetSubnet(ctx, v.External[0].SubnetExtID)
	if err != nil {
		return "Cannot read the transit VPC's external subnet: " + err.Error(), netip.Prefix{}
	}
	if !s.External || s.NAT || s.Type != "VLAN" {
		return s.Name + " must be a VLAN external subnet without NAT: the NAT is in each tenant's subnet", netip.Prefix{}
	}
	_, routes, err := r.Routes(ctx, v.ExtID)
	if err != nil {
		return "Cannot read the transit VPC's routes: " + err.Error(), netip.Prefix{}
	}
	for _, rt := range routes {
		if rt.Destination.Bits() == 0 {
			return "", v.RoutablePrefixes[0]
		}
	}
	return v.Name + " has no default route to its external subnet", netip.Prefix{}
}
