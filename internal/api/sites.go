package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
)

// Errors of the site operations, shared by the API and the admin UI.
var (
	// ErrUnknownSite: no site with that id in the settings.
	ErrUnknownSite = errors.New("no such site")
	// ErrConfirmMismatch: a forget whose confirmation is not the code.
	ErrConfirmMismatch = errors.New("the confirmation must be the site's code")
)

// SiteConflictError says why a site cannot be created or removed (a
// duplicate id or code, tenants still placed on it).
type SiteConflictError struct{ Msg string }

func (e *SiteConflictError) Error() string { return e.Msg }

// patchSites changes the stored document's sites through fn; fn returns
// an error to leave the document unchanged.
func (o *SettingsOptions) patchSites(ctx context.Context, fn func(d *settings.Settings) error) error {
	if o.Service == nil {
		return errSettingsUnavailable
	}
	// A process running from the file adopts it with its first change,
	// as a PUT of the settings does.
	if o.File != nil && !o.Service.Stored(ctx) {
		if err := o.Service.Set(ctx, *o.File); err != nil {
			return err
		}
	}
	var ferr error
	err := o.Service.Patch(ctx, func(d *settings.Settings) {
		before := append([]settings.Site(nil), d.Sites...)
		pass := d.PassthroughSite
		if ferr = fn(d); ferr != nil {
			d.Sites, d.PassthroughSite = before, pass
		}
	})
	if ferr != nil {
		return ferr
	}
	return err
}

var errSettingsUnavailable = errors.New("settings are not available in this process")

// CreateSite adds a site to the stored document (design sites §4.1). The
// id defaults to the code in lower case; TLS verification is on unless
// the caller turned it off. The site takes effect at Apply.
func (o *SettingsOptions) CreateSite(ctx context.Context, in settings.Site) (settings.Site, error) {
	in.Code = strings.TrimSpace(in.Code)
	if in.ID == "" {
		in.ID = settings.SiteIDFromCode(in.Code)
	}
	err := o.patchSites(ctx, func(d *settings.Settings) error {
		for _, s := range d.Sites {
			switch {
			case s.ID == in.ID:
				return &SiteConflictError{Msg: fmt.Sprintf("a site with id %q exists", in.ID)}
			case s.Code == in.Code:
				return &SiteConflictError{Msg: fmt.Sprintf("a site with code %q exists", in.Code)}
			}
		}
		d.Sites = append(d.Sites, in)
		return nil
	})
	return in, err
}

// UpdateSite changes a stored site through fn; the id never changes and
// an empty key keeps the stored one.
func (o *SettingsOptions) UpdateSite(ctx context.Context, id string, fn func(*settings.Site)) (settings.Site, error) {
	var out settings.Site
	err := o.patchSites(ctx, func(d *settings.Settings) error {
		for i := range d.Sites {
			if d.Sites[i].ID != id {
				continue
			}
			key := d.Sites[i].APIKey
			fn(&d.Sites[i])
			d.Sites[i].ID = id
			if d.Sites[i].APIKey == "" {
				d.Sites[i].APIKey = key
			}
			for j := range d.Sites {
				if j != i && d.Sites[j].Code == d.Sites[i].Code {
					return &SiteConflictError{Msg: fmt.Sprintf("a site with code %q exists", d.Sites[i].Code)}
				}
			}
			out = d.Sites[i]
			return nil
		}
		return ErrUnknownSite
	})
	return out, err
}

// removeSite drops the site from the document and from the pass-through.
func removeSite(d *settings.Settings, id string) bool {
	for i := range d.Sites {
		if d.Sites[i].ID == id {
			d.Sites = append(d.Sites[:i:i], d.Sites[i+1:]...)
			if d.PassthroughSite == id {
				d.PassthroughSite = ""
			}
			return true
		}
	}
	return false
}

// DeleteSite removes a site no tenant is placed on (design sites §4.1).
func (o *SettingsOptions) DeleteSite(ctx context.Context, st store.Store, id string) error {
	ps, err := st.ListPlacementsBySite(ctx, id)
	if err != nil {
		return err
	}
	return o.patchSites(ctx, func(d *settings.Settings) error {
		site, ok := d.Site(id)
		if !ok {
			return ErrUnknownSite
		}
		if len(ps) > 0 {
			return &SiteConflictError{Msg: fmt.Sprintf("site %s has tenants: %s", site.Code, strings.Join(tenantIDs(ps), ", "))}
		}
		removeSite(d, id)
		return nil
	})
}

func tenantIDs(ps []store.Placement) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.TenantID)
	}
	return out
}

// ForgetResult says which site was forgotten and what is left in its
// Prism Central for the MSP to clean up if it ever comes back.
type ForgetResult struct {
	Forgotten          string   `json:"forgotten"`
	LeftInPrismCentral []string `json:"left_in_prism_central"`
}

// ForgetSite removes a site whose Prism Central is gone for good (design
// sites §4.1): Portico's records of the site and of the placements on it
// go, no call reaches that Prism Central, and the result lists what
// stays there. confirm must be the site's code; refused while a tenant
// operation runs.
func (o *SettingsOptions) ForgetSite(ctx context.Context, st store.Store, id, confirm string) (ForgetResult, error) {
	cur, _, err := o.Current(ctx)
	if err != nil {
		return ForgetResult{}, err
	}
	site, ok := cur.Site(id)
	if !ok {
		return ForgetResult{}, ErrUnknownSite
	}
	if strings.TrimSpace(confirm) != site.Code {
		return ForgetResult{}, ErrConfirmMismatch
	}
	if o.Busy != nil {
		if n := o.Busy(); n > 0 {
			return ForgetResult{}, &BusyError{N: n}
		}
	}
	ps, err := st.ListPlacementsBySite(ctx, id)
	if err != nil {
		return ForgetResult{}, err
	}
	res := ForgetResult{Forgotten: site.Code, LeftInPrismCentral: []string{}}
	for _, p := range ps {
		if p.ProjectID != "" || p.ProjectExtID != "" {
			res.LeftInPrismCentral = append(res.LeftInPrismCentral, fmt.Sprintf("tenant %s: project %s (%s)", p.TenantID, p.ProjectID, p.ProjectExtID))
		}
		if p.SAMLGroupExtID != "" {
			res.LeftInPrismCentral = append(res.LeftInPrismCentral, fmt.Sprintf("tenant %s: SAML user group %s", p.TenantID, p.SAMLGroupExtID))
		}
		if p.RoleMembershipExtID != "" {
			res.LeftInPrismCentral = append(res.LeftInPrismCentral, fmt.Sprintf("tenant %s: role membership %s", p.TenantID, p.RoleMembershipExtID))
		}
		if p.IdPShared {
			res.LeftInPrismCentral = append(res.LeftInPrismCentral, fmt.Sprintf("tenant %s: the identity provider shared with the project", p.TenantID))
		}
	}
	if site.SAMLIdPName != "" {
		res.LeftInPrismCentral = append(res.LeftInPrismCentral, "identity provider "+site.SAMLIdPName)
	}
	if site.APIKey != "" {
		res.LeftInPrismCentral = append(res.LeftInPrismCentral, "Portico's service account and its API keys")
	}
	if _, err := st.DeletePlacementsBySite(ctx, id); err != nil {
		return ForgetResult{}, err
	}
	if err := o.patchSites(ctx, func(d *settings.Settings) error {
		removeSite(d, id)
		return nil
	}); err != nil {
		return ForgetResult{}, err
	}
	return res, nil
}

// CheckSite checks a candidate connection of a stored site. The stored
// key is sent only to the stored host: a candidate naming another host
// must bring its own key (as the single-Prism Central check did).
func (o *SettingsOptions) CheckSite(ctx context.Context, id string, cand settings.Site, keyGiven bool) (settings.CheckResult, error) {
	cur, _, err := o.Current(ctx)
	if err != nil {
		return settings.CheckResult{}, err
	}
	stored, ok := cur.Site(id)
	if !ok {
		return settings.CheckResult{}, ErrUnknownSite
	}
	if cand.URL == "" {
		cand.URL, cand.TLSVerify, cand.CAPEM = stored.URL, stored.TLSVerify, stored.CAPEM
	}
	if !keyGiven {
		if !sameHost(stored.URL, cand.URL) {
			return settings.CheckResult{Message: "the API key must be given when the Prism Central URL changes: the stored key is only sent to the stored host"}, nil
		}
		cand.APIKey = stored.APIKey
	}
	return o.Checker.PrismCentral(ctx, cand, string(cand.APIKey)), nil
}

// siteJSON is the wire shape of a site; the key is accepted on input
// and never written on output.
type siteJSON struct {
	ID                    string   `json:"id"`
	Code                  string   `json:"code"`
	Name                  string   `json:"name"`
	Order                 int      `json:"order"`
	URL                   string   `json:"url"`
	TLSVerify             *bool    `json:"tls_verify"`
	CAPEM                 string   `json:"ca_pem"`
	APIKey                string   `json:"api_key,omitempty"`
	APIKeySet             bool     `json:"api_key_set"`
	SAMLIdPName           string   `json:"saml_idp_name"`
	SAMLEntityID          string   `json:"saml_entity_id"`
	ClusterExtID          string   `json:"cluster_ext_id"`
	StorageContainerExtID string   `json:"storage_container_ext_id"`
	Tenants               []string `json:"tenants"`
}

func toSiteJSON(s settings.Site, tenants []string) siteJSON {
	tls := s.TLSVerify
	if tenants == nil {
		tenants = []string{}
	}
	return siteJSON{ID: s.ID, Code: s.Code, Name: s.Name, Order: s.Order, URL: s.URL, TLSVerify: &tls, CAPEM: s.CAPEM,
		APIKeySet: s.APIKey != "", SAMLIdPName: s.SAMLIdPName, SAMLEntityID: s.SAMLEntityID,
		ClusterExtID: s.ClusterExtID, StorageContainerExtID: s.StorageContainerExtID, Tenants: tenants}
}

// apply writes the body's editable fields onto s: everything but the id
// and the SAML entity id, which the bootstrap reads from Prism Central.
func (j siteJSON) apply(s *settings.Site) {
	s.Code, s.Name, s.Order = strings.TrimSpace(j.Code), strings.TrimSpace(j.Name), j.Order
	s.URL, s.CAPEM = strings.TrimSpace(j.URL), strings.TrimSpace(j.CAPEM)
	if j.TLSVerify != nil {
		s.TLSVerify = *j.TLSVerify
	}
	s.APIKey = settings.Secret(strings.TrimSpace(j.APIKey))
	s.ClusterExtID, s.StorageContainerExtID = j.ClusterExtID, j.StorageContainerExtID
	// An identity provider created by hand (design §4.5, pre-created
	// path) is named here; empty keeps the one the bootstrap stored.
	if n := strings.TrimSpace(j.SAMLIdPName); n != "" {
		s.SAMLIdPName = n
	}
}

func (h *handler) siteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+Prefix+"sites", h.listSites)
	mux.HandleFunc("POST "+Prefix+"sites", h.createSite)
	mux.HandleFunc("GET "+Prefix+"sites/{id}", h.getSite)
	mux.HandleFunc("PUT "+Prefix+"sites/{id}", h.putSite)
	mux.HandleFunc("DELETE "+Prefix+"sites/{id}", h.deleteSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/forget", h.forgetSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/check", h.checkSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/bootstrap/prism-central", h.bootstrapPrismCentral)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/bootstrap/saml", h.bootstrapSAML)
}

// siteError maps the site operations' errors to statuses.
func (h *handler) siteError(w http.ResponseWriter, err error) {
	var conflict *SiteConflictError
	var busy *BusyError
	var ve *settings.ValidationError
	switch {
	case errors.Is(err, ErrUnknownSite):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &conflict), errors.As(err, &busy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrConfirmMismatch), errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("site operation failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (h *handler) siteTenants(r *http.Request, id string) []string {
	ps, err := h.st.ListPlacementsBySite(r.Context(), id)
	if err != nil {
		return nil
	}
	return tenantIDs(ps)
}

func (h *handler) listSites(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []siteJSON{}
	for _, s := range cur.OrderedSites() {
		out = append(out, toSiteJSON(s, h.siteTenants(r, s.ID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": out})
}

func (h *handler) createSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in siteJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	site := settings.Site{ID: strings.TrimSpace(in.ID), TLSVerify: true}
	in.apply(&site)
	out, err := h.so.CreateSite(r.Context(), site)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Info("site created", "id", out.ID, "code", out.Code, "key_given", out.APIKey != "")
	writeJSON(w, http.StatusCreated, toSiteJSON(out, nil))
}

func (h *handler) getSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s, ok := cur.Site(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, ErrUnknownSite.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSiteJSON(s, h.siteTenants(r, s.ID)))
}

func (h *handler) putSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in siteJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	out, err := h.so.UpdateSite(r.Context(), r.PathValue("id"), in.apply)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Info("site updated", "id", out.ID, "code", out.Code, "key_given", in.APIKey != "")
	writeJSON(w, http.StatusOK, toSiteJSON(out, h.siteTenants(r, out.ID)))
}

func (h *handler) deleteSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.so.DeleteSite(r.Context(), h.st, id); err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Info("site deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) forgetSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	res, err := h.so.ForgetSite(r.Context(), h.st, r.PathValue("id"), in.Confirm)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Warn("site forgotten: its Prism Central was not contacted", "site", res.Forgotten, "left_in_prism_central", res.LeftInPrismCentral)
	writeJSON(w, http.StatusOK, res)
}

func (h *handler) checkSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in siteJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	stored, ok := cur.Site(id)
	if !ok {
		writeError(w, http.StatusNotFound, ErrUnknownSite.Error())
		return
	}
	// The candidate is the stored site with the fields the body gives:
	// an omitted URL, CA or tls_verify keeps the stored value.
	cand := stored
	if u := strings.TrimSpace(in.URL); u != "" {
		cand.URL, cand.CAPEM = u, strings.TrimSpace(in.CAPEM)
	}
	if in.TLSVerify != nil {
		cand.TLSVerify = *in.TLSVerify
	}
	cand.APIKey = settings.Secret(strings.TrimSpace(in.APIKey))
	res, err := h.so.CheckSite(r.Context(), id, cand, in.APIKey != "")
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Info("site check", "id", id, "ok", res.OK, "version", res.Version)
	writeJSON(w, http.StatusOK, res)
}

// bootstrapPrismJSON is the Prism Central bootstrap request body; an
// empty url uses the site's stored connection.
type bootstrapPrismJSON struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	URL            string `json:"url"`
	TLSVerify      *bool  `json:"tls_verify"`
	CAPEM          string `json:"ca_pem"`
	ServiceAccount string `json:"service_account"`
}

func (h *handler) bootstrapPrismCentral(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in bootstrapPrismJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	if _, ok := cur.Site(id); !ok {
		writeError(w, http.StatusNotFound, ErrUnknownSite.Error())
		return
	}
	pin := settings.PrismInput{Username: in.Username, Password: in.Password, URL: in.URL, TLSVerify: true, CAPEM: in.CAPEM, ServiceAccount: in.ServiceAccount}
	if in.TLSVerify != nil {
		pin.TLSVerify = *in.TLSVerify
	}
	res, _, err := h.so.Bootstrapper.PrismCentral(r.Context(), cur, id, pin)
	if err == nil {
		attrs := []any{"site", id, "user", in.Username, "service_account", res.ServiceAccount, "ok", res.OK, "created", res.Created, "version", res.Version}
		if res.OK {
			attrs = append(attrs, "key_expires", res.KeyExpires)
		}
		h.log.Info("bootstrap prism central", attrs...)
	}
	h.bootstrapAnswer(w, r, res, err)
}

// bootstrapSAMLJSON is the SAML trust bootstrap request body.
type bootstrapSAMLJSON struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *handler) bootstrapSAML(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in bootstrapSAMLJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	if _, ok := cur.Site(id); !ok {
		writeError(w, http.StatusNotFound, ErrUnknownSite.Error())
		return
	}
	res, _, err := h.so.Bootstrapper.SAML(r.Context(), h.so.KeycloakURL, cur, id, settings.SAMLInput{Name: in.Name, Username: in.Username, Password: in.Password})
	if err == nil {
		h.log.Info("bootstrap saml", "site", id, "name", res.Name, "ok", res.OK, "used_credentials", res.UsedCredentials, "created", res.Created)
	}
	h.bootstrapAnswer(w, r, res, err)
}
