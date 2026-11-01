package nutanix

import (
	"context"
	"net/netip"
	"testing"
)

const systemProject = "00000000-0000-0000-0000-000000000000"

// fakeGateway is a gateway as GET /gateways answers it (write-up 37).
func fakeGateway(extID, name, project string, local bool, state, message string) map[string]any {
	g := map[string]any{"extId": extID, "name": name, "projectExtId": project}
	if local {
		g["services"] = map[string]any{"$objectType": "networking.v4.config.LocalNetworkServices",
			"localBgpService":  map[string]any{"asn": 65010, "vpcReference": "vpc-1"},
			"serviceAddresses": []any{map[string]any{"ipv4": map[string]any{"value": "10.23.210.195"}}}}
		g["deployment"] = map[string]any{"clusterReference": "cl-1", "interfaces": []any{map[string]any{
			"subnetReference": "sub-1", "ipAddress": map[string]any{"ipv4": map[string]any{"value": "10.23.210.195", "prefixLength": 24}}}}}
		g["vmReference"] = "vm-gw"
		g["installedSoftwareVersion"] = "7.6.0.6"
	} else {
		g["services"] = map[string]any{"$objectType": "networking.v4.config.RemoteNetworkServices",
			"remoteBgpService": map[string]any{"address": map[string]any{"ipv4": map[string]any{"value": "10.23.210.194"}}, "asn": 65020}}
	}
	if state != "" {
		g["status"] = map[string]any{"state": state, "message": message}
	}
	return g
}

func fakeSession(extID, name, local, remote string) map[string]any {
	return map[string]any{"extId": extID, "name": name, "projectExtId": systemProject,
		"localGatewayReference": local, "remoteGatewayReference": remote,
		"localGatewayInterfaceIpAddress":               map[string]any{"ipv4": map[string]any{"value": "10.23.210.195"}},
		"shouldAdvertiseAllExternallyRoutablePrefixes": true,
		"localGateway": map[string]any{"name": "copy"}, "remoteGateway": map[string]any{"name": "copy"},
		"status": map[string]any{"state": "UP"}, "links": []any{}, "$reserved": map[string]any{}, "$objectType": "networking.v4.config.BgpSession", "tenantId": "t-1"}
}

func TestCreateLocalBGPGatewayBodyAndReadBack(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/gateways"] = []any{
		fakeGateway("gw-other", "portico-mi1-bgp", "p-other", true, "UP", ""),
		fakeGateway("gw-1", "portico-mi1-bgp", systemProject, true, "DOWN", "Service nginx is down"),
	}
	c := newClient(t, f, nil)
	g, err := c.CreateLocalBGPGateway(context.Background(), "portico-mi1-bgp", systemProject, "cl-1", "sub-1",
		netip.MustParsePrefix("10.23.210.195/24"), netip.MustParseAddr("10.23.210.254"), 65010, "vpc-1")
	if err != nil {
		t.Fatalf("CreateLocalBGPGateway: %v", err)
	}
	if g.ExtID != "gw-1" || !g.Local || g.ProjectExtID != systemProject || g.State != "DOWN" || g.VMExtID != "vm-gw" || g.ASN != 65010 || g.Address.String() != "10.23.210.195" {
		t.Fatalf("read back = %+v", g)
	}
	b := find(f.requests(), "POST", PathNetworking+"/gateways").Body
	if b["name"] != "portico-mi1-bgp" || b["projectExtId"] != systemProject {
		t.Fatalf("POST body = %v", b)
	}
	dep := b["deployment"].(map[string]any)
	iface := dep["interfaces"].([]any)[0].(map[string]any)
	ip := iface["ipAddress"].(map[string]any)["ipv4"].(map[string]any)
	if dep["clusterReference"] != "cl-1" || iface["subnetReference"] != "sub-1" || ip["value"] != "10.23.210.195" || ip["prefixLength"] != float64(24) ||
		iface["defaultGatewayAddress"].(map[string]any)["ipv4"].(map[string]any)["value"] != "10.23.210.254" {
		t.Fatalf("deployment = %v", dep)
	}
	svc := b["services"].(map[string]any)
	bgp := svc["localBgpService"].(map[string]any)
	if svc["$objectType"] != "networking.v4.config.LocalNetworkServices" || bgp["asn"] != float64(65010) || bgp["vpcReference"] != "vpc-1" ||
		svc["serviceAddresses"].([]any)[0].(map[string]any)["ipv4"].(map[string]any)["value"] != "10.23.210.195" {
		t.Fatalf("services = %v", svc)
	}
}

func TestCreateRemoteBGPGatewayBody(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/gateways"] = []any{fakeGateway("gw-r", "portico-mi1-peer", systemProject, false, "", "")}
	c := newClient(t, f, nil)
	g, err := c.CreateRemoteBGPGateway(context.Background(), "portico-mi1-peer", systemProject, netip.MustParseAddr("10.23.210.194"), 65020)
	if err != nil {
		t.Fatalf("CreateRemoteBGPGateway: %v", err)
	}
	if g.ExtID != "gw-r" || g.Local || g.ASN != 65020 || g.Address.String() != "10.23.210.194" || g.State != "" {
		t.Fatalf("read back = %+v", g)
	}
	b := find(f.requests(), "POST", PathNetworking+"/gateways").Body
	svc := b["services"].(map[string]any)
	bgp := svc["remoteBgpService"].(map[string]any)
	if b["projectExtId"] != systemProject || svc["$objectType"] != "networking.v4.config.RemoteNetworkServices" ||
		bgp["address"].(map[string]any)["ipv4"].(map[string]any)["value"] != "10.23.210.194" || bgp["asn"] != float64(65020) {
		t.Fatalf("POST body = %v", b)
	}
	if _, ok := b["deployment"]; ok {
		t.Fatalf("a remote gateway has no deployment: %v", b)
	}
}

func TestUpdateRemoteGatewaySendsETag(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathNetworking+"/gateways/gw-r"] = fakeGateway("gw-r", "portico-mi1-peer", systemProject, false, "UP", "")
	c := newClient(t, f, nil)
	if err := c.UpdateRemoteGateway(context.Background(), "gw-r", netip.MustParseAddr("10.23.210.1"), 65030); err != nil {
		t.Fatalf("UpdateRemoteGateway: %v", err)
	}
	put := find(f.requests(), "PUT", PathNetworking+"/gateways/gw-r")
	if put == nil || put.Header.Get("If-Match") != f.etag {
		t.Fatalf("put = %+v", put)
	}
	bgp := put.Body["services"].(map[string]any)["remoteBgpService"].(map[string]any)
	if bgp["address"].(map[string]any)["ipv4"].(map[string]any)["value"] != "10.23.210.1" || bgp["asn"] != float64(65030) {
		t.Fatalf("PUT body = %v", put.Body)
	}
	for _, k := range []string{"status", "links", "$reserved", "$objectType", "tenantId", "vmReference", "installedSoftwareVersion"} {
		if _, ok := put.Body[k]; ok {
			t.Fatalf("PUT body carries %s: %v", k, put.Body)
		}
	}
	if f.polls == 0 {
		t.Fatal("the update's task was not waited for")
	}
}

func TestCreateBGPSessionBody(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/bgp-sessions"] = []any{fakeSession("s-1", "portico-mi1-peering", "gw-1", "gw-r")}
	c := newClient(t, f, nil)
	s, err := c.CreateBGPSession(context.Background(), "portico-mi1-peering", systemProject, "gw-1", "gw-r", netip.MustParseAddr("10.23.210.195"))
	if err != nil {
		t.Fatalf("CreateBGPSession: %v", err)
	}
	if s.ExtID != "s-1" || s.LocalExtID != "gw-1" || s.RemoteExtID != "gw-r" || s.State != "UP" || !s.AdvertiseAll {
		t.Fatalf("read back = %+v", s)
	}
	b := find(f.requests(), "POST", PathNetworking+"/bgp-sessions").Body
	if b["name"] != "portico-mi1-peering" || b["projectExtId"] != systemProject || b["localGatewayReference"] != "gw-1" || b["remoteGatewayReference"] != "gw-r" ||
		b["shouldAdvertiseAllExternallyRoutablePrefixes"] != true ||
		b["localGatewayInterfaceIpAddress"].(map[string]any)["ipv4"].(map[string]any)["value"] != "10.23.210.195" {
		t.Fatalf("POST body = %v", b)
	}
}

func TestUpdateBGPSessionSendsETag(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathNetworking+"/bgp-sessions/s-1"] = fakeSession("s-1", "portico-mi1-peering", "gw-1", "gw-r")
	c := newClient(t, f, nil)
	if err := c.UpdateBGPSession(context.Background(), "s-1", "gw-2", "gw-r2"); err != nil {
		t.Fatalf("UpdateBGPSession: %v", err)
	}
	put := find(f.requests(), "PUT", PathNetworking+"/bgp-sessions/s-1")
	if put == nil || put.Header.Get("If-Match") != f.etag || put.Body["localGatewayReference"] != "gw-2" || put.Body["remoteGatewayReference"] != "gw-r2" {
		t.Fatalf("put = %+v", put)
	}
	for _, k := range []string{"localGateway", "remoteGateway", "status", "links", "$reserved", "$objectType", "tenantId"} {
		if _, ok := put.Body[k]; ok {
			t.Fatalf("PUT body carries %s: %v", k, put.Body)
		}
	}
}

func TestBGPRoutesParsesPrefixAndNextHop(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathNetworking+"/bgp-sessions/s-1/bgp-routes"] = []any{map[string]any{
		"destination": map[string]any{"ipv4": map[string]any{"ip": map[string]any{"value": "100.96.0.0"}, "prefixLength": 24}},
		"nexthop":     map[string]any{"nexthopIpAddress": map[string]any{"ipv4": map[string]any{"value": "10.23.210.200"}}},
		"routeType":   "LOCAL"}}
	c := newClient(t, f, nil)
	routes, err := c.BGPRoutes(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("BGPRoutes: %v", err)
	}
	if len(routes) != 1 || routes[0].Prefix.String() != "100.96.0.0/24" || routes[0].NextHop.String() != "10.23.210.200" {
		t.Fatalf("routes = %+v", routes)
	}
}

func TestGatewayStateFromStatus(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathNetworking+"/gateways/gw-1"] = fakeGateway("gw-1", "portico-mi1-bgp", systemProject, true, "DOWN", "Service nginx is down")
	f.objs[PathNetworking+"/gateways/gw-r"] = fakeGateway("gw-r", "portico-mi1-peer", systemProject, false, "", "")
	c := newClient(t, f, nil)
	g, err := c.GetGateway(context.Background(), "gw-1")
	if err != nil {
		t.Fatalf("GetGateway: %v", err)
	}
	if g.State != "DOWN" || g.Message != "Service nginx is down" || g.Version != "7.6.0.6" {
		t.Fatalf("gateway = %+v", g)
	}
	r, err := c.GetGateway(context.Background(), "gw-r")
	if err != nil {
		t.Fatalf("GetGateway: %v", err)
	}
	if r.State != "" || r.Message != "" {
		t.Fatalf("a gateway without status has an empty state: %+v", r)
	}
}

func TestDeleteGatewayAndSessionSendETagAndWait(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathNetworking+"/gateways/gw-1"] = fakeGateway("gw-1", "portico-mi1-bgp", systemProject, true, "UP", "")
	f.objs[PathNetworking+"/bgp-sessions/s-1"] = fakeSession("s-1", "portico-mi1-peering", "gw-1", "gw-r")
	c := newClient(t, f, nil)
	if err := c.DeleteBGPSession(context.Background(), "s-1"); err != nil {
		t.Fatalf("DeleteBGPSession: %v", err)
	}
	if err := c.DeleteGateway(context.Background(), "gw-1"); err != nil {
		t.Fatalf("DeleteGateway: %v", err)
	}
	for _, p := range []string{PathNetworking + "/bgp-sessions/s-1", PathNetworking + "/gateways/gw-1"} {
		if del := find(f.requests(), "DELETE", p); del == nil || del.Header.Get("If-Match") != f.etag {
			t.Fatalf("DELETE %s = %+v", p, del)
		}
	}
}
