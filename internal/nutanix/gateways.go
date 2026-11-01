package nutanix

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
)

// Gateway is a Nutanix network gateway (write-up 37): a local one is the
// BGP gateway VM of a VPC on a VLAN subnet, a remote one stands for the
// router it peers with. State is status.state (UP, DOWN, "" before Prism
// Central reports one), Message its text.
type Gateway struct {
	ExtID, Name, ProjectExtID, VMExtID, Version string
	Local                                       bool
	Address                                     netip.Addr
	ASN                                         int
	State, Message                              string
}

// BGPSession is the eBGP session between a local and a remote gateway.
type BGPSession struct {
	ExtID, Name, LocalExtID, RemoteExtID string
	State, Message                       string
	AdvertiseAll                         bool
}

// BGPRoute is one route of a session: the prefix and the next hop
// Prism Central advertises or learns.
type BGPRoute struct {
	Prefix  netip.Prefix
	NextHop netip.Addr
}

const (
	localServices  = "networking.v4.config.LocalNetworkServices"
	remoteServices = "networking.v4.config.RemoteNetworkServices"
)

type rawIPv4 struct {
	IPv4 struct {
		Value        string `json:"value"`
		PrefixLength int    `json:"prefixLength,omitempty"`
	} `json:"ipv4"`
}

type rawStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type rawGateway struct {
	ExtID        string     `json:"extId"`
	Name         string     `json:"name"`
	ProjectExtID string     `json:"projectExtId"`
	VMReference  string     `json:"vmReference"`
	Version      string     `json:"installedSoftwareVersion"`
	Status       *rawStatus `json:"status"`
	Deployment   *struct {
		Interfaces []struct {
			IPAddress rawIPv4 `json:"ipAddress"`
		} `json:"interfaces"`
	} `json:"deployment"`
	Services struct {
		ObjectType string `json:"$objectType"`
		Local      *struct {
			ASN int `json:"asn"`
		} `json:"localBgpService"`
		Remote *struct {
			Address rawIPv4 `json:"address"`
			ASN     int     `json:"asn"`
		} `json:"remoteBgpService"`
	} `json:"services"`
}

func (r rawGateway) gateway() Gateway {
	g := Gateway{ExtID: r.ExtID, Name: r.Name, ProjectExtID: r.ProjectExtID, VMExtID: r.VMReference, Version: r.Version,
		Local: r.Services.ObjectType == localServices || r.Services.Local != nil}
	if r.Status != nil {
		g.State, g.Message = r.Status.State, r.Status.Message
	}
	switch {
	case r.Services.Local != nil:
		g.ASN = r.Services.Local.ASN
		if r.Deployment != nil && len(r.Deployment.Interfaces) > 0 {
			g.Address, _ = netip.ParseAddr(r.Deployment.Interfaces[0].IPAddress.IPv4.Value)
		}
	case r.Services.Remote != nil:
		g.ASN = r.Services.Remote.ASN
		g.Address, _ = netip.ParseAddr(r.Services.Remote.Address.IPv4.Value)
	}
	return g
}

type rawSession struct {
	ExtID        string     `json:"extId"`
	Name         string     `json:"name"`
	ProjectExtID string     `json:"projectExtId"`
	Local        string     `json:"localGatewayReference"`
	Remote       string     `json:"remoteGatewayReference"`
	AdvertiseAll bool       `json:"shouldAdvertiseAllExternallyRoutablePrefixes"`
	Status       *rawStatus `json:"status"`
}

func (r rawSession) session() BGPSession {
	s := BGPSession{ExtID: r.ExtID, Name: r.Name, LocalExtID: r.Local, RemoteExtID: r.Remote, AdvertiseAll: r.AdvertiseAll}
	if r.Status != nil {
		s.State, s.Message = r.Status.State, r.Status.Message
	}
	return s
}

func ipv4(a netip.Addr) map[string]any {
	return map[string]any{"ipv4": map[string]any{"value": a.String()}}
}

// ListGateways returns every network gateway, local and remote.
func (c *Client) ListGateways(ctx context.Context) ([]Gateway, error) {
	var raw []rawGateway
	if err := c.listAll(ctx, PathNetworking+"/gateways", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Gateway, len(raw))
	for i := range raw {
		out[i] = raw[i].gateway()
	}
	return out, nil
}

// GetGateway reads one gateway, its status included.
func (c *Client) GetGateway(ctx context.Context, extID string) (*Gateway, error) {
	var raw rawGateway
	if _, err := c.do(ctx, http.MethodGet, PathNetworking+"/gateways/"+extID, "", nil, "", &raw); err != nil {
		return nil, err
	}
	g := raw.gateway()
	return &g, nil
}

func gatewayName(g Gateway) string    { return g.Name }
func gatewayProject(g Gateway) string { return g.ProjectExtID }

// CreateLocalBGPGateway creates the BGP gateway VM of a VPC: one
// interface on the VLAN subnet at addr with gw as its default gateway,
// the BGP service for vpcExtID with asn, the interface address as the
// service address. Prism Central downloads the gateway image on the
// first create and deploys the VM; the task ends when the VM is up (up
// to 30 min with the download, write-up 37). The project is mandatory:
// without it the workflow cannot read back the image it registered.
func (c *Client) CreateLocalBGPGateway(ctx context.Context, name, projectExtID, clusterExtID, subnetExtID string, addr netip.Prefix, gw netip.Addr, asn int, vpcExtID string) (*Gateway, error) {
	body := map[string]any{
		"name": name, "description": "Managed by Portico: the BGP gateway of this site's transit VPC", "projectExtId": projectExtID,
		"deployment": map[string]any{
			"clusterReference": clusterExtID,
			"interfaces": []map[string]any{{
				"subnetReference":       subnetExtID,
				"ipAddress":             map[string]any{"ipv4": map[string]any{"value": addr.Addr().String(), "prefixLength": addr.Bits()}},
				"defaultGatewayAddress": ipv4(gw),
			}},
			"shouldSynchronizeSystemDnsServers": true, "shouldSynchronizeSystemNtpServers": true,
		},
		"services": map[string]any{
			"$objectType":      localServices,
			"localBgpService":  map[string]any{"asn": asn, "vpcReference": vpcExtID},
			"serviceAddresses": []map[string]any{ipv4(addr.Addr())},
		},
	}
	return createWithTask(ctx, c, PathNetworking+"/gateways", name, body, c.ListGateways,
		inProject(name, projectExtID, gatewayName, gatewayProject, func(g Gateway) bool { return g.Local }))
}

// CreateRemoteBGPGateway creates the gateway object standing for the
// peer router at addr with asn.
func (c *Client) CreateRemoteBGPGateway(ctx context.Context, name, projectExtID string, addr netip.Addr, asn int) (*Gateway, error) {
	body := map[string]any{
		"name": name, "description": "Managed by Portico: the router this site's transit VPC peers with", "projectExtId": projectExtID,
		"services": map[string]any{
			"$objectType":      remoteServices,
			"remoteBgpService": map[string]any{"address": ipv4(addr), "asn": asn},
		},
	}
	return createWithTask(ctx, c, PathNetworking+"/gateways", name, body, c.ListGateways,
		inProject(name, projectExtID, gatewayName, gatewayProject, func(g Gateway) bool { return !g.Local }))
}

// updateWithTask does GET → change → PUT with If-Match → WaitTask. The
// object is sent back without the fields Prism Central sets (readOnly),
// which a PUT refuses.
func (c *Client) updateWithTask(ctx context.Context, path string, readOnly []string, change func(obj map[string]any)) error {
	var obj map[string]any
	etag, err := c.do(ctx, http.MethodGet, path, "", nil, "", &obj)
	if err != nil {
		return err
	}
	if etag == "" {
		return fmt.Errorf("prism central: GET %s returned no ETag", path)
	}
	for _, k := range readOnly {
		delete(obj, k)
	}
	change(obj)
	var task taskRef
	if _, err := c.do(ctx, http.MethodPut, path, "", obj, etag, &task); err != nil {
		return err
	}
	if task.ExtID == "" {
		return nil
	}
	return c.WaitTask(ctx, task.ExtID)
}

var gatewayReadOnly = []string{"status", "links", "$reserved", "$objectType", "tenantId", "vmReference", "installedSoftwareVersion"}

// UpdateRemoteGateway changes the address and ASN of a remote gateway;
// a session on it follows without being recreated (write-up 38, 2).
func (c *Client) UpdateRemoteGateway(ctx context.Context, extID string, addr netip.Addr, asn int) error {
	return c.updateWithTask(ctx, PathNetworking+"/gateways/"+extID, gatewayReadOnly, func(obj map[string]any) {
		svc, _ := obj["services"].(map[string]any)
		if svc == nil {
			svc = map[string]any{"$objectType": remoteServices}
			obj["services"] = svc
		}
		svc["remoteBgpService"] = map[string]any{"address": ipv4(addr), "asn": asn}
	})
}

// DeleteGateway deletes a gateway (a local one with its VM).
func (c *Client) DeleteGateway(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathNetworking+"/gateways/"+extID)
}

// ListBGPSessions returns every BGP session.
func (c *Client) ListBGPSessions(ctx context.Context) ([]BGPSession, error) {
	var raw []rawSession
	if err := c.listAll(ctx, PathNetworking+"/bgp-sessions", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]BGPSession, len(raw))
	for i := range raw {
		out[i] = raw[i].session()
	}
	return out, nil
}

// GetBGPSession reads one session, its status included.
func (c *Client) GetBGPSession(ctx context.Context, extID string) (*BGPSession, error) {
	var raw rawSession
	if _, err := c.do(ctx, http.MethodGet, PathNetworking+"/bgp-sessions/"+extID, "", nil, "", &raw); err != nil {
		return nil, err
	}
	s := raw.session()
	return &s, nil
}

func sessionName(s BGPSession) string { return s.Name }

// CreateBGPSession creates the session between a local gateway (at
// localAddr on its interface) and a remote one, advertising every
// externally routable prefix of the VPC. Prism Central refuses it while
// the local gateway is not UP (write-up 37); the caller waits first.
func (c *Client) CreateBGPSession(ctx context.Context, name, projectExtID, localExtID, remoteExtID string, localAddr netip.Addr) (*BGPSession, error) {
	body := map[string]any{
		"name": name, "description": "Managed by Portico: the routed prefix of this site's transit VPC, advertised to the router", "projectExtId": projectExtID,
		"localGatewayReference": localExtID, "remoteGatewayReference": remoteExtID,
		"localGatewayInterfaceIpAddress":               ipv4(localAddr),
		"shouldAdvertiseAllExternallyRoutablePrefixes": true,
	}
	return createWithTask(ctx, c, PathNetworking+"/bgp-sessions", name, body, c.ListBGPSessions,
		func(all []BGPSession) *BGPSession {
			for i := range all {
				if all[i].Name == name && all[i].LocalExtID == localExtID && all[i].RemoteExtID == remoteExtID {
					return &all[i]
				}
			}
			return nil
		})
}

var sessionReadOnly = []string{"localGateway", "remoteGateway", "status", "links", "$reserved", "$objectType", "tenantId"}

// UpdateBGPSession points a session at other gateways. Prism Central
// takes the gateway references as immutable (write-up 38, 1): the call
// is kept for the day it does not, callers expect the refusal.
func (c *Client) UpdateBGPSession(ctx context.Context, extID, localExtID, remoteExtID string) error {
	return c.updateWithTask(ctx, PathNetworking+"/bgp-sessions/"+extID, sessionReadOnly, func(obj map[string]any) {
		obj["localGatewayReference"], obj["remoteGatewayReference"] = localExtID, remoteExtID
	})
}

// DeleteBGPSession deletes a session.
func (c *Client) DeleteBGPSession(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathNetworking+"/bgp-sessions/"+extID)
}

// BGPRoutes lists the routes of a session (there is no list across
// sessions, write-up 38).
func (c *Client) BGPRoutes(ctx context.Context, sessionExtID string) ([]BGPRoute, error) {
	var raw []struct {
		Destination struct {
			IPv4 struct {
				IP           struct{ Value string } `json:"ip"`
				PrefixLength int                    `json:"prefixLength"`
			} `json:"ipv4"`
		} `json:"destination"`
		NextHop struct {
			Address rawIPv4 `json:"nexthopIpAddress"`
		} `json:"nexthop"`
	}
	if err := c.listAll(ctx, PathNetworking+"/bgp-sessions/"+sessionExtID+"/bgp-routes", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]BGPRoute, 0, len(raw))
	for _, r := range raw {
		ip, err := netip.ParseAddr(r.Destination.IPv4.IP.Value)
		if err != nil {
			continue
		}
		route := BGPRoute{Prefix: netip.PrefixFrom(ip, r.Destination.IPv4.PrefixLength)}
		route.NextHop, _ = netip.ParseAddr(r.NextHop.Address.IPv4.Value)
		out = append(out, route)
	}
	return out, nil
}
