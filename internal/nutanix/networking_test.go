package nutanix

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

// Fixtures in the v4.4 shapes measured in write-up 34.
func netSubnet(extID, name, project string) map[string]any {
	s := map[string]any{"extId": extID, "name": name, "subnetType": "OVERLAY", "vpcReference": "vpc-1",
		"ipConfig": []any{map[string]any{"ipv4": map[string]any{"ipSubnet": map[string]any{
			"ip": map[string]any{"value": "10.100.1.0"}, "prefixLength": 24}}}}}
	if project != "" {
		s["projectExtId"] = project
	}
	return s
}

func TestCreateVPCBody(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/vpcs"] = []any{map[string]any{"extId": "vpc-1", "name": "portico-acme-vpc", "vpcType": "REGULAR", "projectExtId": "p-1",
		"externalSubnets": []any{map[string]any{"subnetReference": "ext-1", "externalIps": []any{map[string]any{"ipv4": map[string]any{"value": "10.23.211.29"}}}}}}}
	c := newClient(t, f, nil)
	v, err := c.CreateVPC(context.Background(), "portico-acme-vpc", "p-1", "ext-1", []netip.Addr{netip.MustParseAddr("1.1.1.1")})
	if err != nil {
		t.Fatalf("CreateVPC: %v", err)
	}
	if v.ExtID != "vpc-1" || len(v.External) != 1 || v.External[0].IPs[0] != netip.MustParseAddr("10.23.211.29") {
		t.Fatalf("vpc = %+v", v)
	}
	post := find(f.requests(), "POST", PathNetworking+"/vpcs")
	if post == nil {
		t.Fatal("no POST to vpcs")
	}
	b := post.Body
	ext := b["externalSubnets"].([]any)[0].(map[string]any)
	dns := b["commonDhcpOptions"].(map[string]any)["domainNameServers"].([]any)[0].(map[string]any)["ipv4"].(map[string]any)["value"]
	if b["vpcType"] != "REGULAR" || b["projectExtId"] != "p-1" || ext["subnetReference"] != "ext-1" || dns != "1.1.1.1" {
		t.Fatalf("POST body = %v", b)
	}
}

func TestCreateOverlayExternalSubnetHasNoProject(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/subnets"] = []any{netSubnet("oen-1", "portico-acme-ext", "")}
	c := newClient(t, f, nil)
	p := netip.MustParsePrefix("10.23.211.16/28")
	if _, err := c.CreateOverlayExternalSubnet(context.Background(), "portico-acme-ext", "vpc-t", p,
		netip.MustParseAddr("10.23.211.17"), netip.MustParseAddr("10.23.211.18"), netip.MustParseAddr("10.23.211.30")); err != nil {
		t.Fatalf("CreateOverlayExternalSubnet: %v", err)
	}
	b := find(f.requests(), "POST", PathNetworking+"/subnets").Body
	if b["isExternal"] != true || b["isNatEnabled"] != true || b["vpcReference"] != "vpc-t" || b["subnetType"] != "OVERLAY" {
		t.Fatalf("POST body = %v", b)
	}
	if _, ok := b["projectExtId"]; ok {
		t.Fatalf("an overlay external subnet carries no project: %v", b)
	}
	ip := b["ipConfig"].([]any)[0].(map[string]any)["ipv4"].(map[string]any)
	pool := ip["poolList"].([]any)[0].(map[string]any)
	if ip["defaultGatewayIp"].(map[string]any)["value"] != "10.23.211.17" ||
		pool["startIp"].(map[string]any)["value"] != "10.23.211.18" || pool["endIp"].(map[string]any)["value"] != "10.23.211.30" ||
		ip["ipSubnet"].(map[string]any)["prefixLength"] != float64(28) {
		t.Fatalf("ipConfig = %v", ip)
	}
}

func TestShareSubnetSendsETag(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathNetworking+"/subnets/oen-1"] = netSubnet("oen-1", "portico-acme-ext", "")
	c := newClient(t, f, nil)
	if err := c.ShareSubnet(context.Background(), "oen-1", "p-1"); err != nil {
		t.Fatalf("ShareSubnet: %v", err)
	}
	post := find(f.requests(), "POST", PathNetworking+"/subnets/oen-1/$actions/share")
	if post == nil || post.Header.Get("If-Match") != f.etag || post.Body["projectExtId"] != "p-1" {
		t.Fatalf("share = %+v", post)
	}
}

func TestRoutesFiltersByVPC(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/route-tables"] = []any{map[string]any{"extId": "rt-1", "vpcReference": "vpc-1"}}
	f.lists[PathNetworking+"/route-tables/rt-1/routes"] = []any{map[string]any{"extId": "r-1",
		"destination": map[string]any{"ipv4": map[string]any{"ip": map[string]any{"value": "0.0.0.0"}, "prefixLength": 0}},
		"nexthop":     map[string]any{"nexthopType": "EXTERNAL_SUBNET", "nexthopReference": "ext-1"}}}
	c := newClient(t, f, nil)
	rt, routes, err := c.Routes(context.Background(), "vpc-1")
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if rt != "rt-1" || len(routes) != 1 || routes[0].Destination != netip.MustParsePrefix("0.0.0.0/0") || routes[0].NextHopType != "EXTERNAL_SUBNET" || routes[0].NextHopExtID != "ext-1" {
		t.Fatalf("rt = %q, routes = %+v", rt, routes)
	}
	get := find(f.requests(), "GET", PathNetworking+"/route-tables")
	if get == nil || !strings.Contains(get.Query, "%24filter=vpcReference+eq+%27vpc-1%27") {
		t.Fatalf("route-tables query = %+v", get)
	}
}

func TestCreateDefaultRouteBody(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	if err := c.CreateDefaultRoute(context.Background(), "vpc-1", "rt-1", "ext-1", "p-1"); err != nil {
		t.Fatalf("CreateDefaultRoute: %v", err)
	}
	b := find(f.requests(), "POST", PathNetworking+"/route-tables/rt-1/routes").Body
	dst := b["destination"].(map[string]any)["ipv4"].(map[string]any)
	nh := b["nexthop"].(map[string]any)
	if b["name"] != "default" || b["routeType"] != "STATIC" || b["vpcReference"] != "vpc-1" || b["routeTableReference"] != "rt-1" || b["projectExtId"] != "p-1" ||
		dst["ip"].(map[string]any)["value"] != "0.0.0.0" || dst["prefixLength"] != float64(0) ||
		nh["nexthopType"] != "EXTERNAL_SUBNET" || nh["nexthopReference"] != "ext-1" {
		t.Fatalf("POST body = %v", b)
	}
}

func TestFloatingIPAssociation(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/floating-ips"] = []any{
		map[string]any{"extId": "fip-1", "name": "portico-acme-fip-1", "projectExtId": "p-1", "externalSubnetReference": "oen-1",
			"floatingIp": map[string]any{"ipv4": map[string]any{"value": "10.23.211.18"}}, "associationStatus": "ASSOCIATED_TO_VNIC",
			"association": map[string]any{"$objectType": "networking.v4.config.VmNicAssociation", "vmNicReference": "nic-1"}},
		map[string]any{"extId": "fip-2", "name": "portico-acme-fip-2", "floatingIp": map[string]any{"ipv4": map[string]any{"value": "10.23.211.19"}}, "associationStatus": "NOT_ASSOCIATED"},
	}
	c := newClient(t, f, nil)
	fips, err := c.ListFloatingIPs(context.Background())
	if err != nil {
		t.Fatalf("ListFloatingIPs: %v", err)
	}
	if len(fips) != 2 || !fips[0].Associated || fips[0].AssociatedVMNIC != "nic-1" || fips[0].IP != netip.MustParseAddr("10.23.211.18") || fips[0].ExternalSubnetExtID != "oen-1" {
		t.Fatalf("fip-1 = %+v", fips[0])
	}
	if fips[1].Associated || fips[1].AssociatedVMNIC != "" {
		t.Fatalf("fip-2 = %+v", fips[1])
	}
}

// The stored entity, not the request, is returned: Prism Central once kept
// a subnet without the project the request carried (write-up 34).
func TestCreateReturnsStoredEntity(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/subnets"] = []any{netSubnet("s-1", "portico-acme-net", "")}
	c := newClient(t, f, nil)
	s, err := c.CreateOverlaySubnet(context.Background(), "portico-acme-net", "p-1", "vpc-1", netip.MustParsePrefix("10.100.1.0/24"),
		netip.MustParseAddr("10.100.1.1"), netip.MustParseAddr("10.100.1.10"), netip.MustParseAddr("10.100.1.250"))
	if err != nil {
		t.Fatalf("CreateOverlaySubnet: %v", err)
	}
	if find(f.requests(), "POST", PathNetworking+"/subnets").Body["projectExtId"] != "p-1" {
		t.Fatal("the request carries the project")
	}
	if s.ExtID != "s-1" || s.ProjectExtID != "" || s.Prefix != netip.MustParsePrefix("10.100.1.0/24") {
		t.Fatalf("subnet = %+v", s)
	}
}

func v4(s string, bits int) map[string]any {
	return map[string]any{"ipv4": map[string]any{"value": s, "prefixLength": bits}}
}

// The shape read from NTNX-01 (write-up 34).
func TestPrismGatewayParses(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathDomainManagers] = []any{map[string]any{"network": map[string]any{"externalNetworks": []any{map[string]any{
		"defaultGateway": v4("10.23.210.254", 24), "subnetMask": v4("255.255.255.0", 24), "ipAddresses": []any{v4("10.23.210.175", 24)}}}}}}
	c := newClient(t, f, nil)
	gw, own, err := c.PrismGateway(context.Background())
	if err != nil || gw != netip.MustParseAddr("10.23.210.254") || own != netip.MustParsePrefix("10.23.210.175/24") {
		t.Fatalf("PrismGateway = %v %v %v", gw, own, err)
	}
}

func TestClusterAddresses(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathClusterMgmt+"/config/clusters"] = []any{map[string]any{"extId": "c-1", "name": "NTNX-01",
		"config":  map[string]any{"clusterFunction": []string{"AOS"}},
		"network": map[string]any{"externalAddress": v4("10.23.210.170", 32), "externalDataServiceIp": v4("10.23.210.173", 32)},
		"nodes":   map[string]any{"nodeList": []any{map[string]any{"controllerVmIp": v4("10.23.210.172", 32), "hostIp": v4("10.23.210.171", 32)}}}}}
	c := newClient(t, f, nil)
	cl, err := c.ListClusters(context.Background())
	if err != nil || len(cl) != 1 || len(cl[0].Addresses) != 4 || cl[0].Addresses[1] != netip.MustParseAddr("10.23.210.173") {
		t.Fatalf("clusters = %+v, %v", cl, err)
	}
}
