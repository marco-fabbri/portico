package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// Errors of the site operations, shared by the API and the admin UI.
var (
	// ErrUnknownSite: no site with that id in the settings.
	ErrUnknownSite = errors.New("no such site")
	// ErrConfirmMismatch: a forget whose confirmation is not the code.
	ErrConfirmMismatch = errors.New("the confirmation must be the site's code")
	// ErrNeedsTrust: a new https site with verification off and no pin.
	ErrNeedsTrust = errors.New("a new site needs a trusted or pinned certificate")
	// ErrVerifyOff: an edit turning certificate verification off.
	ErrVerifyOff = errors.New("certificate verification cannot be turned off: pin the certificate on the site's page instead")
)

// PinChangedError: the certificate presented now is not the one the MSP
// confirmed; Fingerprint is the one presented.
type PinChangedError struct{ Fingerprint string }

func (e *PinChangedError) Error() string {
	return "the certificate changed again: Prism Central presents " + e.Fingerprint + "; confirm this one"
}

// SiteConflictError says why a site cannot be created or removed (a
// duplicate id or code, tenants still placed on it).
type SiteConflictError struct{ Msg string }

func (e *SiteConflictError) Error() string { return e.Msg }

// RemoveIncompleteError is a removal that failed after some of Portico's
// objects were deleted: it names them, since removing again finds them
// gone and cannot say so.
type RemoveIncompleteError struct {
	Deleted []string
	Err     error
}

func (e *RemoveIncompleteError) Error() string {
	return fmt.Sprintf("%v; already deleted: %s; remove the site again to finish", e.Err, strings.Join(e.Deleted, ", "))
}

func (e *RemoveIncompleteError) Unwrap() error { return e.Err }

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
			key, verified := d.Sites[i].APIKey, d.Sites[i].TLSVerify
			fn(&d.Sites[i])
			if verified && !d.Sites[i].TLSVerify {
				return ErrVerifyOff
			}
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

// AddSite creates a site and, with admin, sets it up in the same call
// (design sites 4.1): without admin the row is left incomplete for the
// manual path. A new https site needs a trusted or pinned certificate.
func (o *SettingsOptions) AddSite(ctx context.Context, in settings.Site, admin *settings.SetupInput) (settings.Site, *settings.SetupResult, error) {
	if strings.HasPrefix(in.URL, "https://") && !in.TLSVerify && in.TLSPin == "" {
		return settings.Site{}, nil, ErrNeedsTrust
	}
	if admin != nil {
		if admin.Username == "" || admin.Password == "" {
			return settings.Site{}, nil, &settings.StateError{Msg: "the Prism Central administrator's username and password are required"}
		}
		if o.Bootstrapper == nil {
			return settings.Site{}, nil, errSettingsUnavailable
		}
		cur, _, err := o.Current(ctx)
		if err != nil {
			return settings.Site{}, nil, err
		}
		if err := settings.SetupReady(cur); err != nil {
			return settings.Site{}, nil, err
		}
	}
	site, err := o.CreateSite(ctx, in)
	if err != nil || admin == nil {
		return site, nil, err
	}
	res, err := o.SetupSite(ctx, site.ID, *admin)
	if err != nil {
		return site, nil, err
	}
	return res.Site, &res, nil
}

// SetupSite runs the setup of a stored site again: "Finish setup" and
// "Repair" (design sites 4.1).
func (o *SettingsOptions) SetupSite(ctx context.Context, id string, in settings.SetupInput) (settings.SetupResult, error) {
	if o.Bootstrapper == nil {
		return settings.SetupResult{}, errSettingsUnavailable
	}
	cur, _, err := o.Current(ctx)
	if err != nil {
		return settings.SetupResult{}, err
	}
	if _, ok := cur.Site(id); !ok {
		return settings.SetupResult{}, ErrUnknownSite
	}
	// The run stores as it goes: a process running from the file adopts
	// it first.
	if err := o.patchSites(ctx, func(*settings.Settings) error { return nil }); err != nil {
		return settings.SetupResult{}, err
	}
	res, err := o.Bootstrapper.SetupSite(ctx, o.KeycloakURL, id, in)
	// A complete run has just checked the key and the trust: the monitor
	// takes it now, not at its next trust check, so the tenants enter at
	// once.
	if err == nil && res.Complete && o.Runtime != nil {
		o.Runtime.Report(id, nil)
	}
	return res, err
}

// PinSite pins the certificate the site presents now, when it is the one
// the MSP confirmed (design sites 4.3); another is a *PinChangedError.
func (o *SettingsOptions) PinSite(ctx context.Context, id, fingerprint string) (settings.Site, error) {
	cur, _, err := o.Current(ctx)
	if err != nil {
		return settings.Site{}, err
	}
	site, ok := cur.Site(id)
	if !ok {
		return settings.Site{}, ErrUnknownSite
	}
	info, err := settings.InspectCertificate(ctx, site.URL, []byte(site.CAPEM))
	if err != nil {
		return settings.Site{}, err
	}
	if strings.ToUpper(strings.TrimSpace(fingerprint)) != info.Fingerprint {
		return settings.Site{}, &PinChangedError{Fingerprint: info.Fingerprint}
	}
	return o.UpdateSite(ctx, id, func(s *settings.Site) { s.TLSPin = info.Fingerprint })
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

// RemoveResult says what removing a site deleted and what it left in the
// site's Prism Central.
type RemoveResult struct {
	Removed            bool     `json:"removed"`
	Deleted            []string `json:"deleted"`
	LeftInPrismCentral []string `json:"left_in_prism_central"`
}

// RemoveSite removes a site no tenant is connected to (sites usability
// design 4.1, "Removing a site"). Portico's own side always goes: the
// site's Keycloak SAML client (a trust left there would stay live), its
// maintenance flag and the row. With admin, Portico's identity provider
// and the stored key are deleted in Prism Central first, once the
// certificate and the credentials pass; without, they are named as left.
// Refused while a tenant operation runs or any connection is on the site,
// a failed one included (its Disconnect cleans Prism Central). A step
// that fails keeps the row, and removing again goes on from there.
func (o *SettingsOptions) RemoveSite(ctx context.Context, st store.Store, id string, admin *settings.SetupInput) (RemoveResult, error) {
	cur, _, err := o.Current(ctx)
	if err != nil {
		return RemoveResult{}, err
	}
	site, ok := cur.Site(id)
	if !ok {
		return RemoveResult{}, ErrUnknownSite
	}
	if o.Busy != nil {
		if n := o.Busy(); n > 0 {
			return RemoveResult{}, &BusyError{N: n}
		}
	}
	ps, err := st.ListPlacementsBySite(ctx, id)
	if err != nil {
		return RemoveResult{}, err
	}
	if len(ps) > 0 {
		conns := make([]string, 0, len(ps))
		for _, p := range ps {
			conns = append(conns, fmt.Sprintf("%s (%s)", p.TenantID, p.Status))
		}
		return RemoveResult{}, &SiteConflictError{Msg: fmt.Sprintf("site %s has tenants: %s", site.Code, strings.Join(conns, ", "))}
	}
	res := RemoveResult{Deleted: []string{}, LeftInPrismCentral: site.PrismObjects()}
	if admin != nil {
		if o.Bootstrapper == nil {
			return RemoveResult{}, errors.New("settings are not available in this process")
		}
		if res.Deleted, res.LeftInPrismCentral, err = o.Bootstrapper.RemoveFromPrismCentral(ctx, cur, id, *admin); err != nil {
			return RemoveResult{}, err
		}
	}
	stopped := func(err error) (RemoveResult, error) {
		if len(res.Deleted) == 0 {
			return RemoveResult{}, err
		}
		return RemoveResult{}, &RemoveIncompleteError{Deleted: res.Deleted, Err: err}
	}
	client, err := o.deleteKeycloakClient(ctx, cur, site)
	if err != nil {
		return stopped(err)
	}
	if client != "" {
		res.Deleted = append(res.Deleted, client)
	}
	if o.Runtime != nil {
		if err := o.Runtime.ClearMaintenance(ctx, id); err != nil {
			return stopped(fmt.Errorf("the maintenance flag was not cleared: %w", err))
		}
	}
	if err := o.patchSites(ctx, func(d *settings.Settings) error {
		if !removeSite(d, id) {
			return ErrUnknownSite
		}
		return nil
	}); err != nil {
		return stopped(err)
	}
	res.Removed = true
	return res, nil
}

// deleteKeycloakClient deletes the site's Keycloak SAML client, named by
// the entity id the setup stored, unless another site names the same
// one. It returns what it deleted.
func (o *SettingsOptions) deleteKeycloakClient(ctx context.Context, cur settings.Settings, site settings.Site) (string, error) {
	if site.SAMLEntityID == "" {
		return "", nil
	}
	for _, s := range cur.Sites {
		if s.ID != site.ID && s.SAMLEntityID == site.SAMLEntityID {
			return "", nil
		}
	}
	kc := settings.KeycloakAdmin(o.KeycloakURL, cur, nil)
	if kc == nil {
		return "", nil
	}
	if err := kc.DeleteClient(ctx, site.SAMLEntityID); err != nil {
		return "", fmt.Errorf("the Keycloak client %s was not deleted: %w", site.SAMLEntityID, err)
	}
	return "Keycloak client " + site.SAMLEntityID, nil
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
// go, with the site's Keycloak client, no call reaches that Prism
// Central, and the result lists what stays there. confirm must be the site's code; refused while a tenant
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
	if _, err := o.deleteKeycloakClient(ctx, cur, site); err != nil {
		return ForgetResult{}, err
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
		cand.URL, cand.TLSVerify, cand.TLSPin, cand.CAPEM = stored.URL, stored.TLSVerify, stored.TLSPin, stored.CAPEM
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
	TLSPin                string   `json:"tls_pin"` // set at creation; then through the pin route only
	CAPEM                 string   `json:"ca_pem"`
	APIKey                string   `json:"api_key,omitempty"`
	APIKeySet             bool     `json:"api_key_set"`
	SAMLIdPName           string   `json:"saml_idp_name"`
	SAMLEntityID          string   `json:"saml_entity_id"`
	ClusterExtID          string   `json:"cluster_ext_id"`
	StorageContainerExtID string   `json:"storage_container_ext_id"`
	Tenants               []string `json:"tenants"`
	// Live state, in running mode only.
	Status      string `json:"status,omitempty"`
	Maintenance bool   `json:"maintenance"`
	Active      int    `json:"active"`
	Idle        int    `json:"idle"`
}

// withLive adds the site's live state when the runtime exists.
func (h *handler) withLive(j siteJSON) siteJSON {
	if h.so == nil || h.so.Runtime == nil {
		return j
	}
	now := time.Now()
	j.Status, j.Maintenance = string(h.so.Runtime.Status(j.ID)), h.so.Runtime.Maintenance(j.ID)
	j.Active, j.Idle = SessionCounts(h.so.Runtime.Sessions(j.ID, now), now)
	return j
}

func toSiteJSON(s settings.Site, tenants []string) siteJSON {
	tls := s.TLSVerify
	if tenants == nil {
		tenants = []string{}
	}
	return siteJSON{ID: s.ID, Code: s.Code, Name: s.Name, Order: s.Order, URL: s.URL, TLSVerify: &tls, TLSPin: s.TLSPin, CAPEM: s.CAPEM,
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
	mux.HandleFunc("POST "+Prefix+"sites/inspect", h.inspectSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/setup", h.setupSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/pin", h.pinSite)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/check", h.checkSite)
	mux.HandleFunc("GET "+Prefix+"sites/{id}/status", h.siteStatus)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/bootstrap/prism-central", h.bootstrapPrismCentral)
	mux.HandleFunc("POST "+Prefix+"sites/{id}/bootstrap/saml", h.bootstrapSAML)
	h.runtimeRoutes(mux)
}

// siteError maps the site operations' errors to statuses.
func (h *handler) siteError(w http.ResponseWriter, err error) {
	var conflict *SiteConflictError
	var busy *BusyError
	var ve *settings.ValidationError
	var se *settings.StateError
	var pin *PinChangedError
	var partial *RemoveIncompleteError
	switch {
	case errors.As(err, &partial):
		h.log.Error("site removal stopped", "err", partial.Err.Error(), "deleted", len(partial.Deleted))
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "deleted": partial.Deleted})
	case errors.Is(err, ErrUnknownSite):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &pin):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "fingerprint": pin.Fingerprint})
	case errors.As(err, &conflict), errors.As(err, &busy), errors.As(err, &se):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrConfirmMismatch), errors.As(err, &ve), errors.Is(err, ErrNeedsTrust), errors.Is(err, ErrVerifyOff):
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
		out = append(out, h.withLive(toSiteJSON(s, h.siteTenants(r, s.ID))))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": out})
}

// createSiteJSON is the create body: a site and, for the one-step path,
// the administrator's one-time credentials.
type createSiteJSON struct {
	siteJSON
	Admin *struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"admin"`
	ServiceAccount string `json:"service_account"`
}

func (h *handler) createSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in createSiteJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	site := settings.Site{ID: strings.TrimSpace(in.ID), TLSVerify: true, TLSPin: strings.ToUpper(strings.TrimSpace(in.TLSPin))}
	in.apply(&site)
	var admin *settings.SetupInput
	if in.Admin != nil {
		admin = &settings.SetupInput{Username: in.Admin.Username, Password: in.Admin.Password, ServiceAccount: in.ServiceAccount}
	}
	out, res, err := h.so.AddSite(r.Context(), site, admin)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Info("site created", "id", out.ID, "code", out.Code, "key_given", in.APIKey != "", "setup", res != nil)
	if res == nil {
		writeJSON(w, http.StatusCreated, toSiteJSON(out, nil))
		return
	}
	writeJSON(w, http.StatusCreated, h.setupJSON(r, *res))
}

// setupJSON is a setup run's answer: the site, the summary lines and
// whether the site is complete.
func (h *handler) setupJSON(r *http.Request, res settings.SetupResult) map[string]any {
	return map[string]any{"site": toSiteJSON(res.Site, h.siteTenants(r, res.Site.ID)), "steps": res.Steps, "complete": res.Complete, "idp_changed": res.IdPChanged}
}

// inspectSite reads the certificate an address presents, before anything
// is stored or any credential travels (design sites 4.1 step 1).
func (h *handler) inspectSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL   string `json:"url"`
		CAPEM string `json:"ca_pem"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	info, err := Inspect(r.Context(), in.URL, in.CAPEM)
	switch {
	case errors.Is(err, ErrInvalidURL):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, info)
	}
}

// ErrInvalidURL is an address that is not an https URL.
var ErrInvalidURL = errors.New("not a valid https URL")

// Inspect reads the certificate an address presents, before anything is
// stored or any credential travels (design sites 4.1 step 1). The error
// is ErrInvalidURL, or why nothing answers.
func Inspect(ctx context.Context, rawURL, caPEM string) (settings.CertInfo, error) {
	raw := strings.TrimSpace(rawURL)
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return settings.CertInfo{}, fmt.Errorf("the Prism Central URL %q is %w", raw, ErrInvalidURL)
	}
	return settings.InspectCertificate(ctx, raw, []byte(strings.TrimSpace(caPEM)))
}

// setupSite runs the setup again with an administrator's one-time
// credentials (Finish setup, Repair).
func (h *handler) setupSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		ServiceAccount string `json:"service_account"`
		Pin            string `json:"pin"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	res, err := h.so.SetupSite(r.Context(), id, settings.SetupInput{Username: in.Username, Password: in.Password, ServiceAccount: in.ServiceAccount, Pin: in.Pin})
	if err != nil {
		h.siteError(w, err)
		return
	}
	res.Steps = append(res.Steps, RepairLines(r.Context(), h.svc, id, res)...)
	writeJSON(w, http.StatusOK, h.setupJSON(r, res))
}

// RepairLines binds the tenants connected to a site again after a
// complete setup run (design sites 4.1, "Repair on a site in use"), one
// summary line per tenant. It runs after every complete run, not only
// when the trust was recreated: "Repair again" for a tenant that failed
// is the same call. Without a tenant service (setup mode) it does
// nothing.
func RepairLines(ctx context.Context, tenants *tenant.Service, siteID string, res settings.SetupResult) []settings.StepLine {
	if tenants == nil || !res.Complete {
		return nil
	}
	lines, err := tenants.RepairSite(ctx, siteID, res.OldIdPExtID)
	if err != nil {
		return []settings.StepLine{{Name: "tenants", Status: "failed", Message: err.Error() + "; Repair again"}}
	}
	out := make([]settings.StepLine, 0, len(lines))
	for _, l := range lines {
		line := settings.StepLine{Name: "tenant " + l.Tenant, Status: "ok", Message: l.Message}
		if !l.OK {
			line.Status, line.Message = "failed", l.Message+"; Repair again"
		}
		out = append(out, line)
	}
	return out
}

// pinSite pins the certificate presented now, when it is the one the MSP
// confirmed.
func (h *handler) pinSite(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	out, err := h.so.PinSite(r.Context(), id, in.Fingerprint)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Warn("site certificate pinned", "site", id, "fingerprint", out.TLSPin)
	writeJSON(w, http.StatusOK, toSiteJSON(out, h.siteTenants(r, id)))
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
	writeJSON(w, http.StatusOK, h.withLive(toSiteJSON(s, h.siteTenants(r, s.ID))))
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
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	var admin *settings.SetupInput
	if in.Username != "" || in.Password != "" {
		admin = &settings.SetupInput{Username: in.Username, Password: in.Password}
	}
	id := r.PathValue("id")
	res, err := h.so.RemoveSite(r.Context(), h.st, id, admin)
	if err != nil {
		h.siteError(w, err)
		return
	}
	h.log.Warn("site removed", "id", id, "with_credentials", admin != nil, "deleted", res.Deleted, "left_in_prism_central", res.LeftInPrismCentral)
	writeJSON(w, http.StatusOK, res)
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

// SiteStatusView is a site's status read from Prism Central and Keycloak
// (sites usability design 4.2). Status, reason and new_fingerprint are
// the monitor's, in running mode only.
type SiteStatusView struct {
	Status         string `json:"status,omitempty"`
	Reason         string `json:"reason,omitempty"`
	NewFingerprint string `json:"new_fingerprint,omitempty"`
	settings.StatusReport
}

// siteStatus checks the site live and, in running mode, gives the
// outcome to the monitor so the tile and new entries follow at once.
func (h *handler) siteStatus(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	out, err := h.so.CheckStatus(r.Context(), id, h.log)
	if err != nil {
		h.siteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CheckStatus checks a site in Prism Central and Keycloak now and, in
// running mode, gives the outcome to the monitor so the tile and new
// entries follow at once (sites usability design 4.2).
func (o *SettingsOptions) CheckStatus(ctx context.Context, id string, log *slog.Logger) (SiteStatusView, error) {
	cur, _, err := o.Current(ctx)
	if err != nil {
		return SiteStatusView{}, err
	}
	site, ok := cur.Site(id)
	if !ok {
		return SiteStatusView{}, ErrUnknownSite
	}
	rep := settings.SiteStatus(ctx, settings.KeycloakAdmin(o.KeycloakURL, cur, log), site, log)
	out := SiteStatusView{StatusReport: rep}
	if o.Runtime != nil {
		o.Runtime.Report(id, rep.MonitorError())
		d := o.Runtime.Detail(id)
		out.Status, out.Reason, out.NewFingerprint = string(d.Status), d.Reason, d.NewFingerprint
	}
	log.Info("site status", "site", id, "status", out.Status, "connection", rep.Connection.OK, "service_account", rep.ServiceAccount.OK, "saml", rep.SAML.OK, "defaults", rep.Defaults.OK)
	return out, nil
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
