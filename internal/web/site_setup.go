package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/settings"
)

// siteNewData is "Add a site" as typed so far; secrets are never echoed.
type siteNewData struct {
	S              settings.Site
	ServiceAccount string
}

// certData is the certificate fragment of "Add a site" (sites usability
// design 4.1 step 1). Step 2, the administrator, shows only when Trusted
// or Confirmed: the credentials never travel to a server Portico has not
// authenticated.
type certData struct {
	Error     string
	Info      settings.CertInfo
	Confirmed bool
}

// summaryData is the summary of a setup run, one line per step, or of a
// site added from a pasted key (Manual).
type summaryData struct {
	Error    string
	Site     settings.Site
	Steps    []settings.StepLine
	Complete bool
	Manual   bool
}

// statusRow is one row of the site's status card.
type statusRow struct {
	Name string
	settings.StatusPart
}

// statusData is the site's status card (sites usability design 4.2): the
// four rows as Prism Central and Keycloak answered, and the action they
// call for.
type statusData struct {
	S     settings.Site
	Error string
	View  api.SiteStatusView
	Rows  []statusRow
	// Finish and Repair offer the setup form; CertChanged is the
	// fingerprint to confirm, PinCurrent the one to pin (Unverified: a
	// site stored with verification off, design 4.3).
	Finish, Repair bool
	CertChanged    string
	PinCurrent     string
	Unverified     bool
}

func (h *Handler) siteSetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup/sites/new", h.siteNew)
	mux.HandleFunc("POST /setup/sites/new/inspect", h.siteInspect)
	mux.HandleFunc("POST /setup/sites/new", h.siteCreate)
	mux.HandleFunc("POST /setup/sites/{id}/status", h.siteStatusCard)
	mux.HandleFunc("POST /setup/sites/{id}/setup", h.siteSetup)
	mux.HandleFunc("POST /setup/sites/{id}/pin", h.sitePin)
}

func (h *Handler) siteNew(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.current(w, r); !ok {
		return
	}
	h.render(w, r, http.StatusOK, "site_new", "Add a site", siteNewData{ServiceAccount: "portico"}, "")
}

// siteInspect reads the certificate the typed address presents: one
// handshake, no credential. The checkbox posts here again; a fingerprint
// that is no longer the one presented is not confirmed.
func (h *Handler) siteInspect(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	var d certData
	info, err := api.Inspect(r.Context(), r.FormValue("url"), r.FormValue("ca_pem"))
	if err != nil {
		d.Error = err.Error()
	} else {
		d.Info = info
		d.Confirmed = !info.Trusted && r.FormValue("confirmed") == "on" && normPin(r.FormValue("pin")) == info.Fingerprint
	}
	h.fragment(w, r, "site_new", "site_cert", d)
}

func normPin(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// siteCreate adds the site and sets it up in one run with the
// administrator's credentials; under "Advanced", a pasted API key adds
// it without them (design 4.5 path 1). The answer is the summary
// fragment, also on a refusal, so the page shows it in place.
func (h *Handler) siteCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	cur, _, ok := h.current(w, r)
	if !ok {
		return
	}
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	in := settings.Site{ID: v("id"), TLSVerify: true}
	if err := siteFromForm(r, &in); err != nil {
		h.fragment(w, r, "site_new", "site_summary", summaryData{Error: err.Error()})
		return
	}
	if v("order") == "" {
		for _, s := range cur.Sites {
			in.Order = max(in.Order, s.Order+1)
		}
	}
	if r.FormValue("confirmed") == "on" {
		in.TLSPin = normPin(r.FormValue("pin"))
	}
	var admin *settings.SetupInput
	if r.FormValue("password") != "" || in.APIKey == "" {
		admin = &settings.SetupInput{Username: v("username"), Password: r.FormValue("password"), ServiceAccount: v("service_account")}
	}
	site, res, err := h.setup.AddSite(r.Context(), in, admin)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, api.ErrNeedsTrust) {
			msg = "Read the certificate and confirm it first."
		}
		h.fragment(w, r, "site_new", "site_summary", summaryData{Error: msg})
		return
	}
	d := summaryData{Site: site, Manual: res == nil}
	if res != nil {
		d.Steps, d.Complete = res.Steps, res.Complete
	}
	h.log.Info("site created", "via", "ui", "id", site.ID, "code", site.Code, "setup", res != nil, "complete", d.Complete)
	h.fragment(w, r, "site_new", "site_summary", d)
}

// siteStatusCard checks the site in Prism Central and Keycloak now, as
// the API's status does, and renders the card.
func (h *Handler) siteStatusCard(w http.ResponseWriter, r *http.Request) {
	// The check reports to the monitor: an effect, so a POST with the token.
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	d := statusData{S: s}
	view, err := h.setup.CheckStatus(r.Context(), s.ID, h.log)
	if err != nil {
		d.Error = err.Error()
		h.fragment(w, r, "site", "site_status", d)
		return
	}
	rep := view.StatusReport
	d.View = view
	d.Rows = []statusRow{{"Connection", rep.Connection.StatusPart}, {"Service account", rep.ServiceAccount}, {"SAML trust", rep.SAML}, {"Defaults", rep.Defaults}}
	for _, row := range d.Rows {
		d.Finish = d.Finish || row.Action == "Finish setup"
		d.Repair = d.Repair || row.Action == "Repair"
	}
	d.Repair = d.Repair && !d.Finish
	switch {
	case rep.Connection.Action == "Confirm the new certificate":
		d.CertChanged = rep.Connection.Fingerprint
	case rep.Connection.Action == "Pin the current certificate":
		d.PinCurrent = rep.Connection.Fingerprint
	case rep.Connection.Trust == "unverified" && rep.Connection.Fingerprint != "":
		d.PinCurrent, d.Unverified = rep.Connection.Fingerprint, true
	}
	h.fragment(w, r, "site", "site_status", d)
}

// siteSetup is Finish setup and Repair: the setup run again with the
// administrator's one-time credentials, then the connected tenants bound
// again (design 4.1), as the API's setup does.
func (h *Handler) siteSetup(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	res, err := h.setup.SetupSite(r.Context(), s.ID, settings.SetupInput{Username: v("username"), Password: r.FormValue("password"), ServiceAccount: v("service_account")})
	if err != nil {
		h.fragment(w, r, "site", "site_summary", summaryData{Error: err.Error()})
		return
	}
	res.Steps = append(res.Steps, api.RepairLines(r.Context(), h.svc, s.ID, res)...)
	h.log.Info("site setup", "via", "ui", "site", s.ID, "complete", res.Complete, "idp_changed", res.IdPChanged)
	h.fragment(w, r, "site", "site_summary", summaryData{Site: res.Site, Steps: res.Steps, Complete: res.Complete})
}

// sitePin pins the certificate presented now, when it is the one the MSP
// compared (design 4.3); one that changed again is named for a new
// comparison.
func (h *Handler) sitePin(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	out, err := h.setup.PinSite(r.Context(), s.ID, r.FormValue("fingerprint"))
	var changed *api.PinChangedError
	switch {
	case errors.As(err, &changed):
		h.renderSite(w, r, http.StatusConflict, s, "The certificate changed again: Prism Central now presents "+changed.Fingerprint+". Compare it and confirm it again.")
		return
	case err != nil:
		h.renderSite(w, r, siteStatus(err), s, err.Error())
		return
	}
	h.log.Warn("site certificate pinned", "via", "ui", "site", s.ID, "fingerprint", out.TLSPin)
	http.Redirect(w, r, "/setup/sites/"+url.PathEscape(s.ID)+"?notice="+url.QueryEscape("Certificate pinned: "+out.TLSPin+"."), http.StatusSeeOther)
}
