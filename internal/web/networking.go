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

	"github.com/marco-fabbri/portico/internal/netready"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/settings"
)

// NetReader opens the read side of a site's Prism Central for the
// Networking card, with the site's service account key.
type NetReader func(s settings.Site, log *slog.Logger) (netready.Reader, error)

// siteNetReader is NetReader against the site itself.
func siteNetReader(s settings.Site, log *slog.Logger) (netready.Reader, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := proxy.PrismTLSFor(u.Hostname(), s.Trust())
	if err != nil {
		return nil, err
	}
	if s.APIKey == "" {
		return nil, errors.New("no service account key is stored: Finish setup first")
	}
	return nutanix.New(s.URL, string(s.APIKey), tlsCfg, log)
}

// networkingData is the site's Networking card (tenant networking design
// 4.1): the checks as Prism Central answers now, the transit VPCs to pick
// from, and the outcome of a save.
type networkingData struct {
	S       settings.Site
	Error   string
	Checks  []netready.Check
	Choices []netready.Choice
	Ready   bool
	Used    int    // connections to the site holding a /28
	Notice  string // a save's outcome
	Refused string // why the switch stays off
}

func (h *Handler) networkingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup/sites/{id}/networking", h.siteNetworking)
	mux.HandleFunc("POST /setup/sites/{id}/networking", h.siteNetworkingSave)
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/network", h.running(h.networkAction("create")))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/network/retry", h.running(h.networkAction("retry")))
	mux.HandleFunc("POST /tenants/{id}/floating-ips", h.running(h.tenantAllowance))
}

// usedSlices counts the connections to the site that hold a /28.
func (h *Handler) usedSlices(ctx context.Context, siteID string) int {
	ps, err := h.st.ListPlacementsBySite(ctx, siteID)
	if err != nil {
		h.log.Warn("placements unreadable", "site", siteID, "err", err.Error())
		return 0
	}
	n := 0
	for _, p := range ps {
		if p.TransitPrefix != "" {
			n++
		}
	}
	return n
}

// networking reads the card for the site with n as its network.
func (h *Handler) networking(ctx context.Context, s settings.Site, n settings.SiteNetwork) networkingData {
	d := networkingData{S: s, Used: h.usedSlices(ctx, s.ID)}
	d.S.Network = n
	reader := h.netReader
	if reader == nil {
		reader = siteNetReader
	}
	r, err := reader(s, h.log)
	if err != nil {
		d.Error = "Prism Central cannot be read: " + err.Error()
		return d
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if d.Choices, err = netready.TransitChoices(ctx, r); err != nil {
		d.Error = "The VPCs cannot be read: " + err.Error()
	}
	d.Checks = netready.Evaluate(ctx, r, n, s.ServiceAccountExtID, d.Used)
	d.Ready = netready.Ready(d.Checks)
	return d
}

func (h *Handler) siteNetworking(w http.ResponseWriter, r *http.Request) {
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	h.writeNetworking(w, r, http.StatusOK, h.networking(r.Context(), s, s.Network))
}

// networkFromForm reads the card's fields over cur.
func networkFromForm(r *http.Request, cur settings.SiteNetwork) (settings.SiteNetwork, error) {
	n := cur
	n.TransitVPCExtID = strings.TrimSpace(r.PostFormValue("transit_vpc_ext_id"))
	n.IntegratedConfirmed = r.PostFormValue("integrated") == "on"
	n.Enabled = r.PostFormValue("enabled") == "on"
	n.DNS = nil
	for _, f := range strings.FieldsFunc(r.PostFormValue("dns"), func(c rune) bool { return c == ',' || c == ' ' }) {
		a, err := netip.ParseAddr(f)
		if err != nil || !a.Is4() {
			return cur, fmt.Errorf("%q is not an IPv4 address", f)
		}
		n.DNS = append(n.DNS, a.String())
	}
	return n, nil
}

// siteNetworkingSave stores the card. The switch goes on only when every
// check passes; otherwise the rest is saved and the switch stays off. The
// transit VPC does not change while connections hold a /28 of it.
func (h *Handler) siteNetworkingSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	n, err := networkFromForm(r, s.Network)
	if err != nil {
		d := h.networking(r.Context(), s, s.Network)
		d.Refused = err.Error()
		h.writeNetworking(w, r, http.StatusBadRequest, d)
		return
	}
	if n.TransitVPCExtID != s.Network.TransitVPCExtID && s.Network.TransitVPCExtID != "" {
		if used := h.usedSlices(r.Context(), s.ID); used > 0 {
			d := h.networking(r.Context(), s, s.Network)
			d.Refused = fmt.Sprintf("%d tenant connection(s) have a ready network on the transit VPC: disconnect them before picking another one", used)
			h.writeNetworking(w, r, http.StatusConflict, d)
			return
		}
	}
	status := http.StatusOK
	d := h.networking(r.Context(), s, n)
	if n.Enabled && !d.Ready {
		n.Enabled = false
		status = http.StatusUnprocessableEntity
		var open []string
		for _, c := range d.Checks {
			if c.Status == netready.Failed || c.Status == netready.Todo {
				open = append(open, c.Name+": "+c.Message)
			}
		}
		d.Refused = "Saved, but the ready network stays off until every check passes: " + strings.Join(open, "; ")
	}
	out, err := h.setup.UpdateSite(r.Context(), s.ID, func(site *settings.Site) { site.Network = n })
	if err != nil {
		d.Refused = err.Error()
		h.writeNetworking(w, r, siteStatus(err), d)
		return
	}
	d.S = out
	if status == http.StatusOK {
		d.Notice = "Saved."
		if n.Enabled {
			d.Notice = "Saved: new connections to this site get a ready network."
		}
	}
	h.log.Info("site networking saved", "via", "ui", "site", s.ID, "enabled", n.Enabled, "transit", n.TransitVPCExtID)
	h.writeNetworking(w, r, status, d)
}

func (h *Handler) writeNetworking(w http.ResponseWriter, r *http.Request, status int, d networkingData) {
	d.S.APIKey = "" // the card never carries the key
	p := page{Version: h.version, Data: d}
	if s, ok := h.session(r); ok {
		p.Admin, p.CSRF = "admin", s.CSRF
	}
	h.writeTemplate(w, swapStatus(r, status), "site", "site_networking", p)
}

// networkAction is Create ready network and Retry network on one
// connection (design 4.3).
func (h *Handler) networkAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireCSRF(w, r) {
			return
		}
		t, ok := h.loadTenant(w, r)
		if !ok {
			return
		}
		site := r.PathValue("site")
		var err error
		if action == "create" {
			err = h.svc.CreateNetwork(r.Context(), t.ID, site)
		} else {
			err = h.svc.RetryNetwork(r.Context(), t.ID, site)
		}
		if err != nil {
			h.detailWithError(w, r, t, statusOf(err), err.Error())
			return
		}
		h.log.Info("tenant ready network "+action, "via", "ui", "tenant", t.ID, "site", site)
		http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID)+"#site-"+url.PathEscape(site), http.StatusSeeOther)
	}
}

// tenantAllowance sets the tenant's floating IPs per site.
func (h *Handler) tenantAllowance(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("allowance")))
	if err != nil {
		h.detailWithError(w, r, t, http.StatusBadRequest, "the floating IPs per site must be a number from 0 to 12")
		return
	}
	kept, err := h.svc.SetAllowance(r.Context(), t.ID, n)
	if err != nil {
		h.detailWithError(w, r, t, statusOf(err), err.Error())
		return
	}
	h.log.Info("tenant floating ip allowance set", "via", "ui", "tenant", t.ID, "allowance", n, "kept", kept)
	msg := fmt.Sprintf("Floating IPs per site: %d.", n)
	if kept > 0 {
		msg += fmt.Sprintf(" %d associated with a VM were kept: dissociate them in Prism Central, then save again.", kept)
	}
	h.notice(w, msg)
	http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
}
