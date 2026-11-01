package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/netready"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/peering"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
)

// Peerer is the peering service as the Routing section uses it
// (internal/peering.Service).
type Peerer interface {
	Build(ctx context.Context, site settings.Site, p settings.SitePeering, edge peering.EdgeBuilder) (settings.SitePeering, error)
	Refresh(ctx context.Context, p settings.SitePeering) (settings.SitePeering, error)
	ReplaceEdge(ctx context.Context, site settings.Site, p settings.SitePeering, edge peering.EdgeBuilder) (settings.SitePeering, error)
	MoveToRouter(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error)
	MoveToEdge(ctx context.Context, site settings.Site, p settings.SitePeering, edge peering.EdgeBuilder) (settings.SitePeering, error)
	BackToEdge(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error)
	DeleteEdge(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error)
	TearDown(ctx context.Context, site settings.Site, p settings.SitePeering) (settings.SitePeering, error)
}

// PeeringOpener opens a site's peering service and its edge builder;
// save stores the peering as the service changes it.
type PeeringOpener func(site settings.Site, save func(context.Context, settings.SitePeering) error, log *slog.Logger) (Peerer, peering.EdgeBuilder, error)

// peeringRunBound bounds one operation in the background: the gateway's
// first create downloads its image (up to 30 min, write-up 37).
const peeringRunBound = 45 * time.Minute

// sitePeering is PeeringOpener against the site itself.
func (h *Handler) sitePeering(site settings.Site, save func(context.Context, settings.SitePeering) error, log *slog.Logger) (Peerer, peering.EdgeBuilder, error) {
	c, err := siteClient(site, log)
	if err != nil {
		return nil, nil, err
	}
	return peering.New(c, log, save), peering.Edge{PC: c, Version: h.version}, nil
}

// routingView is the Routing section of the card: what the MSP's network
// still needs, whether an operation runs, and the actions the state allows.
type routingView struct {
	SiteID                                                                                string
	P                                                                                     settings.SitePeering
	Kind                                                                                  string
	Building                                                                              bool
	LinesTitle                                                                            string
	Lines                                                                                 []string
	DefaultGW                                                                             string // the VLAN's router, offered as the gateway's default gateway
	CanRoute, CanRepair, CanMoveRouter, CanMoveEdge, CanBackToEdge, CanReplace, CanDelete bool
}

func (h *Handler) routingView(s settings.Site, n settings.SiteNetwork, choices []netready.Choice, checks []netready.Check) routingView {
	p := n.Peering
	v := routingView{SiteID: s.ID, P: p, Kind: p.KindOrStatic(), Building: p.State == settings.PeeringBuilding}
	// The session as Prism Central reports it now, not as last stored.
	sessionUp := p.State == settings.PeeringUp
	for _, c := range checks {
		if c.Name == "bgp session" {
			sessionUp = c.Status == netready.OK
		}
	}
	if p.GatewayASN == 0 {
		v.P.GatewayASN = 65010
	}
	if p.EdgeASN == 0 {
		v.P.EdgeASN = 65020
	}
	if p.GatewayGW.IsValid() {
		v.DefaultGW = p.GatewayGW.String()
	}
	var routed netip.Prefix
	for _, c := range choices {
		if c.VPCExtID == n.TransitVPCExtID {
			routed = c.RoutedPrefix
		}
	}
	built := p.LocalGatewayExtID != ""
	switch v.Kind {
	case settings.PeerRouter:
		if !sessionUp && built {
			v.LinesTitle, v.Lines = "On your router", netready.RouterLines(p)
		}
		v.CanRepair = built && !v.Building
		v.CanMoveEdge = built && !v.Building && p.EdgeAddr.IsValid()
		v.CanBackToEdge = built && !v.Building && p.EdgeVMExtID != ""
		v.CanDelete = !v.Building && p.EdgeVMExtID != ""
		if p.EdgeVMExtID != "" {
			// The kept edge is off: a static route to it is dead, and on a
			// router that runs FRR a static route is preferred to the one
			// learnt by BGP whatever its metric (write-up 39).
			v.LinesTitle, v.Lines = "On your core", []string{"Remove the static route " + netready.CoreRoute(routed, p) + ": your router is the peer and learns the prefix by BGP"}
		}
	case settings.PeerEdge:
		if !p.RouteAdded && built {
			v.LinesTitle, v.Lines = "On your core", []string{"ip route " + netready.CoreRoute(routed, p)}
			v.CanRoute = true
		}
		v.CanRepair = built && !v.Building
		v.CanReplace = built && !v.Building
		v.CanMoveRouter = built && !v.Building && p.RouterAddr.IsValid() && p.RouterASN != 0
	}
	return v
}

// peeringFromForm reads the Routing section's fields over cur: the kind,
// the addresses and the ASNs; what Portico built and its state stay.
func peeringFromForm(r *http.Request, cur settings.SitePeering) (settings.SitePeering, error) {
	p := cur
	p.Kind = strings.TrimSpace(r.PostFormValue("peer"))
	if p.Kind == settings.PeerStatic {
		p.Kind = ""
	}
	addr := func(name string) (netip.Addr, error) {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			return netip.Addr{}, nil
		}
		// "10.0.210.196/24" is accepted as the address: the fields hold
		// addresses, the mask is the gateway address's.
		a, err := netip.ParseAddr(v)
		if strings.Contains(v, "/") {
			var pfx netip.Prefix
			if pfx, err = netip.ParsePrefix(v); err == nil {
				a = pfx.Addr()
			}
		}
		if err != nil || !a.Is4() {
			return netip.Addr{}, fmt.Errorf("%q is not an IPv4 address", v)
		}
		return a, nil
	}
	asn := func(name string, def int) (int, error) {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 4294967295 {
			return 0, fmt.Errorf("%q is not an ASN", v)
		}
		return n, nil
	}
	var err error
	var gw netip.Addr
	if gw, err = addr("gateway_addr"); err != nil {
		return cur, err
	}
	if gw.IsValid() {
		bits := 24
		if cur.GatewayAddr.IsValid() {
			bits = cur.GatewayAddr.Bits()
		}
		p.GatewayAddr = netip.PrefixFrom(gw, bits)
	} else {
		p.GatewayAddr = netip.Prefix{}
	}
	if p.GatewayGW, err = addr("gateway_gw"); err != nil {
		return cur, err
	}
	if p.GatewayASN, err = asn("gateway_asn", cur.GatewayASN); err != nil {
		return cur, err
	}
	if p.RouterAddr, err = addr("router_addr"); err != nil {
		return cur, err
	}
	if p.RouterASN, err = asn("router_asn", cur.RouterASN); err != nil {
		return cur, err
	}
	if p.EdgeAddr, err = addr("edge_addr"); err != nil {
		return cur, err
	}
	if p.EdgeASN, err = asn("edge_asn", cur.EdgeASN); err != nil {
		return cur, err
	}
	// A peer's address and ASN go together: a form without the address
	// clears the pair (every filled-in peer is validated).
	if !p.RouterAddr.IsValid() {
		p.RouterASN = 0
	}
	if !p.EdgeAddr.IsValid() {
		p.EdgeASN = 0
	}
	return p, nil
}

// externalSubnet reads the transit VPC's external subnet: the VLAN the
// peering's addresses are on, its pool and its router.
func externalSubnet(ctx context.Context, r netready.Reader, vpcExtID string) (*nutanix.Subnet, error) {
	vpcs, err := r.ListVPCs(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range vpcs {
		if v.ExtID != vpcExtID {
			continue
		}
		if len(v.External) == 0 {
			return nil, errors.New("the transit VPC has no external subnet")
		}
		return r.GetSubnet(ctx, v.External[0].SubnetExtID)
	}
	return nil, errors.New("the transit VPC is not in Prism Central")
}

// routingSave stores the Routing section and runs what it implies in the
// background: a build for a new peer, a move between the router and the
// edge, a tear-down back to static when nothing needs the peering.
func (h *Handler) routingSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	refuse := func(status int, why string) {
		d := h.networking(r.Context(), s, s.Network)
		d.Refused = why
		h.writeNetworking(w, r, status, d)
	}
	cur := s.Network.Peering
	p, err := peeringFromForm(r, cur)
	if err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	if cur.State == settings.PeeringBuilding {
		refuse(http.StatusConflict, "An operation on the peering is running: wait for it")
		return
	}
	kind := p.KindOrStatic()
	if kind != settings.PeerStatic {
		if s.Network.TransitVPCExtID == "" {
			refuse(http.StatusBadRequest, "Pick the transit VPC and save the card first: the peering is built on its external subnet")
			return
		}
		reader := h.netReader
		if reader == nil {
			reader = siteNetReader
		}
		rd, err := reader(s, h.log)
		if err != nil {
			refuse(http.StatusBadGateway, "Prism Central cannot be read: "+err.Error())
			return
		}
		ext, err := externalSubnet(r.Context(), rd, s.Network.TransitVPCExtID)
		if err != nil {
			refuse(http.StatusBadGateway, "The transit VPC's external subnet cannot be read: "+err.Error())
			return
		}
		if p.GatewayAddr.IsValid() && ext.Prefix.IsValid() {
			p.GatewayAddr = netip.PrefixFrom(p.GatewayAddr.Addr(), ext.Prefix.Bits())
		}
		if !p.GatewayGW.IsValid() {
			p.GatewayGW = cur.GatewayGW
			if !p.GatewayGW.IsValid() && ext.Gateway != p.EdgeAddr {
				p.GatewayGW = ext.Gateway
			}
		}
		if err := p.Validate(ext.Prefix, ext.Pool); err != nil {
			refuse(http.StatusBadRequest, err.Error())
			return
		}
	}
	built := cur.LocalGatewayExtID != ""
	curKind := cur.KindOrStatic()
	var op func(context.Context, Peerer, peering.EdgeBuilder, settings.SitePeering) (settings.SitePeering, error)
	name := "build"
	switch {
	case kind == settings.PeerStatic && built:
		if h.peeringNeeded(r.Context(), s.ID, s.Network.Enabled) {
			refuse(http.StatusConflict, "The peering is in use: switch the ready network off and disconnect the tenants' ready networks before going back to a static route")
			return
		}
		name, op = "tear-down", func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.TearDown(ctx, s, p)
		}
	case kind == settings.PeerStatic:
		out, err := h.setup.UpdateSite(r.Context(), s.ID, func(site *settings.Site) { site.Network.Peering = p })
		if err != nil {
			refuse(siteStatus(err), err.Error())
			return
		}
		d := h.networking(r.Context(), out, out.Network)
		d.Notice = "Saved: the routed prefix is routed by your router with a static route."
		h.writeNetworking(w, r, http.StatusOK, d)
		return
	case built && curKind == settings.PeerEdge && kind == settings.PeerRouter:
		// The stored kind follows the remote gateway: the service changes
		// it once Prism Central peers with the router, so a move that
		// fails before leaves the edge as the peer (write-up 39).
		p.Kind = curKind
		name, op = "move-router", func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.MoveToRouter(ctx, s, p)
		}
	case built && curKind == settings.PeerRouter && kind == settings.PeerEdge:
		if cur.EdgeVMExtID != "" && cur.EdgeAddr.IsValid() && p.EdgeAddr != cur.EdgeAddr {
			refuse(http.StatusConflict, "The kept edge router was built with "+cur.EdgeAddr.String()+": Delete the edge router first, then move to the edge router with the new address")
			return
		}
		p.Kind = curKind
		name, op = "move-edge", func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.MoveToEdge(ctx, s, p, edge)
		}
	case built && kind == settings.PeerEdge && cur.EdgeAddr.IsValid() && p.EdgeAddr != cur.EdgeAddr:
		// A new address is a Replace with it (design 4.3): the edge is
		// rebuilt, the remote gateway and the subnet's gateway follow.
		name, op = "replace", func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.ReplaceEdge(ctx, s, p, edge)
		}
	default:
		op = func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.Build(ctx, s, p, edge)
		}
	}
	s.Network.Peering = p
	if err := h.runPeering(&s, name, op); err != nil {
		refuse(siteStatus(err), err.Error())
		return
	}
	h.log.Info("site routing saved", "via", "ui", "site", s.ID, "peer", kind, "op", name)
	d := h.networking(r.Context(), s, s.Network)
	d.Notice = "Saved: the peering is being " + map[string]string{"build": "built", "move-router": "moved to your router", "move-edge": "moved to the edge router", "replace": "replaced with the new address", "tear-down": "removed"}[name] + "."
	h.writeNetworking(w, r, http.StatusOK, d)
}

// routingAction runs one action of the Routing section.
func (h *Handler) routingAction(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	p := s.Network.Peering
	action := r.PathValue("action")
	card := func(status int, notice, refused string) {
		d := h.networking(r.Context(), s, s.Network)
		d.Notice, d.Refused = notice, refused
		h.writeNetworking(w, r, status, d)
	}
	// Every action, the route tick too: the operation stores its own copy
	// of the peering as it goes and would write the tick over.
	if p.State == settings.PeeringBuilding {
		card(http.StatusConflict, "", "An operation on the peering is running: wait for it")
		return
	}
	var op func(context.Context, Peerer, peering.EdgeBuilder, settings.SitePeering) (settings.SitePeering, error)
	notice := ""
	switch action {
	case "route-added":
		out, err := h.setup.UpdateSite(r.Context(), s.ID, func(site *settings.Site) { site.Network.Peering.RouteAdded = true })
		if err != nil {
			card(siteStatus(err), "", err.Error())
			return
		}
		s = out
		card(http.StatusOK, "Noted: the static route on your core is in place.", "")
		return
	case "repair":
		if p.KindOrStatic() == settings.PeerStatic {
			card(http.StatusConflict, "", "A static route is not repaired by Portico")
			return
		}
		notice, op = "Repair runs: the missing objects are built again.", func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.Build(ctx, s, p, edge)
		}
	case "move-router":
		if !p.RouterAddr.IsValid() || p.RouterASN == 0 {
			card(http.StatusBadRequest, "", "Save your router's address and ASN first")
			return
		}
		notice, op = "Moving the peering to your router.", func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.MoveToRouter(ctx, s, p)
		}
	case "move-edge":
		if !p.EdgeAddr.IsValid() || p.EdgeASN == 0 {
			card(http.StatusBadRequest, "", "Save the edge router's address first")
			return
		}
		notice, op = "Moving the peering to the edge router.", func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.MoveToEdge(ctx, s, p, edge)
		}
	case "back-to-edge":
		if p.EdgeVMExtID == "" {
			card(http.StatusConflict, "", "No edge router is kept")
			return
		}
		notice, op = "Moving the peering back to the edge router.", func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.BackToEdge(ctx, s, p)
		}
	case "replace-edge", "delete-edge":
		h.routingConfirmed(w, r, s, action)
		return
	default:
		h.render(w, r, http.StatusNotFound, "notfound", "Not found", nil, "")
		return
	}
	if err := h.runPeering(&s, action, op); err != nil {
		card(siteStatus(err), "", err.Error())
		return
	}
	h.log.Info("site routing action", "via", "ui", "site", s.ID, "action", action)
	card(http.StatusOK, notice, "")
}

// routingConfirmed is Replace the edge router and Delete the edge router:
// a confirmation page first, the operation on the confirmed POST.
func (h *Handler) routingConfirmed(w http.ResponseWriter, r *http.Request, s settings.Site, action string) {
	p := s.Network.Peering
	path := "/setup/sites/" + url.PathEscape(s.ID) + "/routing/" + action
	back := "/setup/sites/" + url.PathEscape(s.ID)
	d := confirmData{Back: back, Confirmed: true, Danger: true}
	var op func(context.Context, Peerer, peering.EdgeBuilder, settings.SitePeering) (settings.SitePeering, error)
	switch action {
	case "replace-edge":
		if p.KindOrStatic() != settings.PeerEdge || p.LocalGatewayExtID == "" {
			h.render(w, r, http.StatusConflict, "confirm", "Replace the edge router", confirmData{Title: "Replace the edge router?", Text: "The edge router is not the peer of this site.", Back: back}, "")
			return
		}
		d.Title, d.Button = "Replace the edge router of "+s.Code+"?", "Replace"
		d.Text = "Portico powers the edge router off, deletes it and builds it again from the current image with the same address: about three minutes without the routed prefix, until the BGP session is up again. The static route on your core does not change. Pick a window."
		op = func(ctx context.Context, pr Peerer, edge peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.ReplaceEdge(ctx, s, p, edge)
		}
	case "delete-edge":
		if p.KindOrStatic() == settings.PeerEdge {
			h.render(w, r, http.StatusConflict, "confirm", "Delete the edge router", confirmData{Title: "Delete the edge router?", Text: "The edge router is the peer of this site: move the peering to your router first.", Back: back}, "")
			return
		}
		if p.EdgeVMExtID == "" {
			h.render(w, r, http.StatusConflict, "confirm", "Delete the edge router", confirmData{Title: "Delete the edge router?", Text: "No edge router is kept on this site.", Back: back}, "")
			return
		}
		d.Title, d.Button = "Delete the edge router of "+s.Code+"?", "Delete"
		d.Text = "The edge router kept powered off is deleted; the way back to it is gone. Remove the static route to it from your core."
		op = func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
			return pr.DeleteEdge(ctx, s, p)
		}
	}
	if r.PostFormValue("confirmed") != "on" {
		d.Action = path
		h.confirm(w, r, d)
		return
	}
	if err := h.runPeering(&s, action, op); err != nil {
		h.render(w, r, siteStatus(err), "confirm", d.Title, confirmData{Title: d.Title, Text: err.Error(), Back: back}, "")
		return
	}
	h.log.Info("site routing action", "via", "ui", "site", s.ID, "action", action)
	http.Redirect(w, r, back+"#card-networking", http.StatusSeeOther)
}

// runPeering runs op on the site's peering in the background, one at a
// time per site, with the peering stored as it changes.
func (h *Handler) runPeering(s *settings.Site, name string, op func(context.Context, Peerer, peering.EdgeBuilder, settings.SitePeering) (settings.SitePeering, error)) error {
	if h.setup == nil {
		return errors.New("the settings are not stored yet")
	}
	h.peeringMu.Lock()
	if h.peeringRuns[s.ID] {
		h.peeringMu.Unlock()
		return &api.BusyError{N: 1}
	}
	h.peeringRuns[s.ID] = true
	h.peeringMu.Unlock()
	save := func(ctx context.Context, p settings.SitePeering) error {
		_, err := h.setup.UpdateSite(ctx, s.ID, func(site *settings.Site) { site.Network.Peering = p })
		return err
	}
	open := h.peering
	if open == nil {
		open = h.sitePeering
	}
	p := s.Network.Peering
	p.State, p.Error = settings.PeeringBuilding, ""
	if err := save(context.Background(), p); err != nil {
		h.peeringDone(s.ID)
		return err
	}
	s.Network.Peering = p
	site := *s
	h.peeringWG.Add(1)
	go func() {
		defer h.peeringWG.Done()
		defer h.peeringDone(site.ID)
		ctx, cancel := context.WithTimeout(context.Background(), peeringRunBound)
		defer cancel()
		pr, edge, err := open(site, save, h.log)
		var out settings.SitePeering
		if err == nil {
			out, err = op(ctx, pr, edge, p)
		}
		if err != nil {
			h.log.Warn("site peering "+name+" failed", "site", site.ID, "err", err.Error())
			cur, gerr := h.setup.Service.Get(context.Background())
			if gerr == nil {
				if stored, ok := cur.Site(site.ID); ok && stored.Network.Peering.State == settings.PeeringBuilding {
					failed := stored.Network.Peering
					failed.State, failed.Error, failed.StateAt = settings.PeeringFailed, err.Error(), time.Now().UTC()
					if serr := save(context.Background(), failed); serr != nil {
						h.log.Warn("site peering state not saved", "site", site.ID, "err", serr.Error())
					}
				}
			}
			return
		}
		// The service saved as it went; the result is stored once more so
		// a service that saves nothing (a fake, a future one) still lands.
		if serr := save(context.Background(), out); serr != nil {
			h.log.Warn("site peering state not saved", "site", site.ID, "err", serr.Error())
		}
		h.log.Info("site peering "+name+" done", "site", site.ID, "state", out.State)
	}()
	return nil
}

func (h *Handler) peeringDone(siteID string) {
	h.peeringMu.Lock()
	delete(h.peeringRuns, siteID)
	h.peeringMu.Unlock()
}

// WaitPeering waits for the peering operations in the background.
func (h *Handler) WaitPeering() { h.peeringWG.Wait() }

// peeringNeeded reports whether the site's peering must stay: the switch
// is on, or a connection to the site holds a ready network (design 4.6).
func (h *Handler) peeringNeeded(ctx context.Context, siteID string, enabled bool) bool {
	if enabled {
		return true
	}
	ps, err := h.st.ListPlacementsBySite(ctx, siteID)
	if err != nil {
		h.log.Warn("placements unreadable", "site", siteID, "err", err.Error())
		return true
	}
	for _, p := range ps {
		if p.NetState == store.NetReady || p.NetState == store.NetBuilding {
			return true
		}
	}
	return false
}

// lastNetworkGone is the tenant service's hook: the last ready network on
// the site is gone; with the switch off, the peering goes too.
func (h *Handler) lastNetworkGone(ctx context.Context, siteID string) {
	if h.setup == nil || h.setup.Service == nil {
		return
	}
	cur, err := h.setup.Service.Get(ctx)
	if err != nil {
		return
	}
	s, ok := cur.Site(siteID)
	if !ok || s.Network.Enabled || s.Network.Peering.LocalGatewayExtID == "" || h.peeringNeeded(ctx, siteID, false) {
		return
	}
	if err := h.runPeering(&s, "tear-down", func(ctx context.Context, pr Peerer, _ peering.EdgeBuilder, p settings.SitePeering) (settings.SitePeering, error) {
		return pr.TearDown(ctx, s, p)
	}); err != nil {
		h.log.Warn("site peering tear-down not started", "site", siteID, "err", err.Error())
	}
}
