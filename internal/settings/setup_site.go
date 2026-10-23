package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
)

// SetupInput is "Add a site" step 2 and "Finish setup": the one-time
// credentials of a Prism Central administrator (design sites 4.1), the
// service account's name, and the fingerprint the MSP confirmed on the
// page. Pin is required when the site's certificate is not trusted by
// the system roots or its CA.
type SetupInput struct {
	Username, Password, ServiceAccount, Pin string
}

// StepLine is one line of the setup summary; Status is ok, failed,
// skipped or kept.
type StepLine struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// SetupResult is what a setup run leaves: the stored site, one line per
// step that ran, and whether the site is complete. IdPChanged says the
// stored identity provider was replaced by a new one, OldIdPExtID (Repair
// moves the tenants off it).
type SetupResult struct {
	Site        Site       `json:"-"`
	Steps       []StepLine `json:"steps"`
	Complete    bool       `json:"complete"`
	IdPChanged  bool       `json:"idp_changed"`
	OldIdPExtID string     `json:"-"`
}

// SetupDeadline bounds one setup run.
const SetupDeadline = 2 * time.Minute

// setupOperations are the Prism Central operations the setup's creations
// use (write-up 26, measured on pc.7.6): the service account, its key,
// the policy membership and the identity provider.
var setupOperations = []string{"Create_User", "Create_User_Key", "Update_Authorization_Policy", "Create_Saml_Identity_Provider"}

// SetupReady says whether the document is ready to set up sites: the
// public names and the Keycloak realm with its admin client.
func SetupReady(s Settings) error {
	if strings.TrimSpace(s.PrismHost) == "" || strings.TrimSpace(s.IDHost) == "" || s.Keycloak.Realm == "" || s.Keycloak.AdminClientSecret == "" {
		return &StateError{Msg: "the public names and the Keycloak realm with its admin client must be set first"}
	}
	return nil
}

// SetupSite sets a site up in one run (design sites 4.1): certificate,
// version, rights, identity provider, service account, SAML trust,
// defaults, live. A failure stops the run at that step, except the
// defaults: what was done is stored, and a run again goes on from there
// (every step is idempotent). The credentials live in the Prism Central
// client of this call only. The error is for invalid input, an unknown
// site or a document not ready; a failed step is a result.
func (b *Bootstrapper) SetupSite(ctx context.Context, keycloakURL, siteID string, in SetupInput) (SetupResult, error) {
	if b.svc == nil {
		return SetupResult{}, errors.New("settings are not available in this process")
	}
	name := strings.TrimSpace(in.ServiceAccount)
	if name == "" {
		name = defaultServiceAccount
	}
	switch {
	case in.Username == "" || in.Password == "":
		return SetupResult{}, &StateError{Msg: "the Prism Central username and password are required"}
	case !reServiceAccount.MatchString(name):
		return SetupResult{}, &StateError{Msg: "the service account name must be lower-case letters, digits, dots, dashes and underscores"}
	}
	ctx, cancel := context.WithTimeout(ctx, SetupDeadline)
	defer cancel()
	doc, err := b.svc.Get(ctx)
	if err != nil {
		return SetupResult{}, err
	}
	site, ok := doc.Site(siteID)
	if !ok {
		return SetupResult{}, &StateError{Msg: fmt.Sprintf("no site with id %q", siteID)}
	}
	if err := SetupReady(doc); err != nil {
		return SetupResult{}, err
	}
	r := &setupRun{b: b, ctx: ctx, doc: doc, site: site, in: in, name: name, keycloakURL: keycloakURL}
	r.run()
	final, err := b.svc.Get(ctx)
	if err == nil {
		if s, ok := final.Site(siteID); ok {
			r.site = s
		}
	}
	res := SetupResult{Site: r.site, Steps: r.steps, Complete: r.site.Complete(), IdPChanged: r.oldIdP != "", OldIdPExtID: r.oldIdP}
	b.log.Info("site setup", "site", siteID, "complete", res.Complete, "steps", len(res.Steps), "idp_changed", res.IdPChanged)
	return res, nil
}

// setupRun is one run's state: the site as stored so far and the clients
// built from the credentials.
type setupRun struct {
	b           *Bootstrapper
	ctx         context.Context
	doc         Settings
	site        Site
	in          SetupInput
	name        string
	keycloakURL string

	steps      []StepLine
	pc         *nutanix.Client
	clusters   []nutanix.Cluster
	descriptor []byte
	own        *nutanix.SAMLIdP
	oldIdP     string
}

func (r *setupRun) line(name, status, msg string) bool {
	r.steps = append(r.steps, StepLine{Name: name, Status: status, Message: msg})
	r.b.log.Info("site setup step", "site", r.site.ID, "step", name, "status", status)
	return status != "failed"
}

// patch stores fn's change of this site and keeps the local copy in step;
// a site removed meanwhile is an error.
func (r *setupRun) patch(fn func(*Site)) error {
	found := false
	_, err := r.b.store(r.ctx, func(d *Settings) {
		if i := d.siteIndex(r.site.ID); i >= 0 {
			found = true
			fn(&d.Sites[i])
		}
	})
	switch {
	case err != nil:
		return fmt.Errorf("the settings were not stored: %w", err)
	case !found:
		return errors.New("the site was removed while the setup ran")
	}
	fn(&r.site)
	return nil
}

func (r *setupRun) run() {
	for _, step := range []func() bool{r.certificate, r.version, r.rights, r.identityProvider, r.serviceAccount, r.samlTrust} {
		if !step() {
			return
		}
	}
	r.defaults() // a site without defaults is served; the status card says Choose
	r.live()
}

// certificate: one handshake, before any credential travels (design
// sites 4.3); a pin the MSP confirmed now is stored at once.
func (r *setupRun) certificate() bool {
	const step = "certificate"
	pc, trusted, msg, err := adminClient(r.ctx, r.site, r.in, r.b.log)
	if err != nil {
		return r.line(step, "failed", err.Error())
	}
	if trusted.TLSPin != r.site.TLSPin || trusted.TLSVerify != r.site.TLSVerify {
		if err := r.patch(func(s *Site) { s.TLSPin, s.TLSVerify = trusted.TLSPin, trusted.TLSVerify }); err != nil {
			return r.line(step, "failed", err.Error())
		}
	}
	r.pc = pc
	return r.line(step, "ok", msg)
}

// adminClient is the setup's first step, which removal shares: one
// handshake before any credential travels (design sites 4.3). The
// certificate is trusted, or it is the stored pin, or it is the one the
// MSP confirmed (in.Pin), which pins it. It returns a Prism Central
// client with the credentials, the site with its trust as it must be
// stored, and the step's message.
func adminClient(ctx context.Context, site Site, in SetupInput, log *slog.Logger) (*nutanix.Client, Site, string, error) {
	info, err := InspectCertificate(ctx, site.URL, []byte(site.CAPEM))
	if err != nil {
		return nil, site, "", err
	}
	confirmed := strings.ToUpper(strings.TrimSpace(in.Pin))
	var msg string
	switch {
	case site.TLSPin != "" && info.Fingerprint == site.TLSPin:
		msg = "the pinned certificate is presented (" + info.Fingerprint + ")"
	case confirmed != "" && confirmed == info.Fingerprint:
		site.TLSPin, msg = info.Fingerprint, "certificate confirmed and pinned ("+info.Fingerprint+")"
	case site.TLSPin == "" && info.Trusted:
		site.TLSVerify, msg = true, "the certificate is trusted: issued by "+info.Issuer
	case site.TLSPin != "":
		return nil, site, "", errors.New("certificate changed: Prism Central presents " + info.Fingerprint + "; the certificate is not confirmed")
	default:
		return nil, site, "", errors.New("the certificate is not confirmed: Prism Central presents " + info.Fingerprint)
	}
	u, _ := url.Parse(site.URL)
	tlsCfg, err := proxy.PrismTLSFor(u.Hostname(), site.Trust())
	if err != nil {
		return nil, site, "", errors.New("the CA certificate is not usable: " + err.Error())
	}
	pc, err := nutanix.NewWithPassword(site.URL, in.Username, in.Password, tlsCfg, log)
	if err != nil {
		return nil, site, "", err
	}
	return pc, site, msg, nil
}

// version: the clusters list with the credentials; pc.7.6 or later.
func (r *setupRun) version() bool {
	const step = "version"
	clusters, err := r.pc.ListClusters(r.ctx)
	if err != nil {
		return r.line(step, "failed", credentialRefusal(r.site.URL, err))
	}
	r.clusters = clusters
	v := nutanix.PrismCentralVersionOf(clusters)
	switch {
	case v == "":
		return r.line(step, "ok", "Prism Central answered; it reports no version")
	case !VersionAtLeast(v, minMajor, minMinor):
		return r.line(step, "failed", fmt.Sprintf("Prism Central %s is older than pc.7.6: Portico needs pc.7.6 or later", v))
	}
	return r.line(step, "ok", "Prism Central "+v)
}

// rights: best effort (write-up 24 item 2). Only a local account's
// rights are read: a directory account holds them through groups, which
// the read cannot follow. The creations stay the authoritative check.
func (r *setupRun) rights() bool {
	const step = "rights"
	const unread = "could not read the account's rights; the next steps will tell"
	user, err := r.pc.FindUser(r.ctx, r.in.Username)
	if err != nil {
		return r.line(step, "skipped", unread)
	}
	if user.UserType != "LOCAL" {
		return r.line(step, "skipped", "the account's rights come from its directory and are not read; the next steps will tell")
	}
	missing, err := r.pc.MissingOperations(r.ctx, user.ExtID, setupOperations)
	switch {
	case err != nil:
		return r.line(step, "skipped", unread)
	case len(missing) > 0:
		return r.line(step, "failed", "the account lacks "+strings.Join(missing, ", "))
	}
	return r.line(step, "ok", "the account holds "+strings.Join(setupOperations, ", "))
}

// idpName is the identity provider's name: the stored one, else the
// default.
func (r *setupRun) idpName() string {
	if r.site.SAMLIdPName != "" {
		return r.site.SAMLIdPName
	}
	return defaultIdPName
}

// identityProvider: read-only. Keycloak's descriptor and Prism Central's
// providers; another provider stops the run before anything is created.
func (r *setupRun) identityProvider() bool {
	const step = "identity-provider"
	descriptor, msg := samlDescriptor(r.ctx, KeycloakAdmin(r.keycloakURL, r.doc, r.b.log), r.doc)
	if msg != "" {
		return r.line(step, "failed", msg)
	}
	r.descriptor = descriptor
	idps, err := r.pc.ListSAMLIdPs(r.ctx)
	if err != nil {
		return r.line(step, "failed", credentialRefusal(r.site.URL, err))
	}
	own, foreign := ownIdP(idps, r.doc, r.site, r.idpName())
	if foreign != "" {
		return r.line(step, "failed", fmt.Sprintf("an identity provider named %s already exists in Prism Central: Portico does not adopt an existing one", foreign))
	}
	r.own = own
	if own != nil {
		return r.line(step, "ok", "Portico's identity provider "+own.Name+" is present and is reused")
	}
	return r.line(step, "ok", "no identity provider yet: Portico creates "+r.idpName())
}

// serviceAccount: create or reuse the account, issue a key unless the
// stored one still works, join the policy; the key and both ids are
// stored, or the new key is revoked.
func (r *setupRun) serviceAccount() bool {
	const step = "service-account"
	acct, msg := r.b.serviceAccount(r.ctx, r.pc, r.doc, r.site, r.name, true)
	if msg != "" {
		return r.line(step, "failed", msg)
	}
	if acct.Kept {
		if err := r.patch(func(s *Site) { s.ServiceAccountExtID = acct.User.ExtID }); err != nil {
			return r.line(step, "failed", err.Error())
		}
		return r.line(step, "kept", "service account "+r.name+": the stored key is accepted")
	}
	key := acct.Key
	if err := r.patch(func(s *Site) {
		s.APIKey, s.ServiceAccountExtID, s.APIKeyExtID = Secret(key.Key), acct.User.ExtID, key.ExtID
	}); err != nil {
		acct.discard()
		return r.line(step, "failed", err.Error()+", so the new key was revoked")
	}
	return r.line(step, "ok", fmt.Sprintf("service account %s with a new key %s (%s)", r.name, key.Name, strings.Join(acct.Created, ", ")))
}

// samlTrust: Portico's identity provider (created, or the one found) and
// the Keycloak client; the name, id and entity id are stored.
func (r *setupRun) samlTrust() bool {
	const step = "saml-trust"
	name := r.idpName()
	if r.own != nil {
		name = r.own.Name
	}
	trust, msg := r.b.samlTrust(r.ctx, r.pc, KeycloakAdmin(r.keycloakURL, r.doc, r.b.log), r.doc, r.site.ID, name, r.own, r.descriptor, true)
	if msg != "" {
		return r.line(step, "failed", msg)
	}
	old := r.site.SAMLIdPExtID
	if err := r.patch(func(s *Site) { s.SAMLIdPName, s.SAMLIdPExtID, s.SAMLEntityID = name, trust.IdPExtID, trust.EntityID }); err != nil {
		return r.line(step, "failed", err.Error())
	}
	if old != "" && old != trust.IdPExtID {
		r.oldIdP = old
	}
	if len(trust.Created) == 0 {
		return r.line(step, "ok", fmt.Sprintf("identity provider %s and Keycloak client %s in place", name, trust.EntityID))
	}
	return r.line(step, "ok", "created "+strings.Join(trust.Created, ", "))
}

// defaults: the cluster and container a new tenant uses, picked as
// PickDefaults says; a choice that still exists is kept.
func (r *setupRun) defaults() bool {
	const step = "defaults"
	containers, err := r.pc.ListStorageContainers(r.ctx)
	if err != nil {
		return r.line(step, "failed", "the storage containers could not be read: "+errClass(err))
	}
	var cluster *nutanix.Cluster
	for i := range r.clusters {
		if r.clusters[i].IsAOS && r.clusters[i].ExtID == r.site.ClusterExtID {
			cluster = &r.clusters[i]
		}
	}
	if cluster == nil {
		id, _ := PickDefaults(r.clusters, nil)
		for i := range r.clusters {
			if r.clusters[i].ExtID == id {
				cluster = &r.clusters[i]
			}
		}
	}
	if cluster == nil {
		return r.line(step, "failed", "Prism Central has no AOS cluster")
	}
	scID := ""
	for _, sc := range containers {
		if sc.ExtID == r.site.StorageContainerExtID && sc.ClusterExtID == cluster.ExtID {
			scID = sc.ExtID
		}
	}
	if scID == "" {
		var cand []nutanix.StorageContainer
		for _, sc := range containers {
			if sc.ClusterExtID == cluster.ExtID && !sc.IsSystem && !sc.IsObjects {
				cand = append(cand, sc)
			}
		}
		if len(cand) > 1 {
			for i := range cand {
				// Best effort: an unread free space counts as none.
				cand[i].FreeBytes, _ = r.pc.ContainerFreeBytes(r.ctx, cand[i].ExtID)
			}
		}
		_, scID = PickDefaults([]nutanix.Cluster{*cluster}, cand)
	}
	if scID == "" {
		return r.line(step, "failed", "no storage container to use on cluster "+cluster.Name)
	}
	scName := scID
	for _, sc := range containers {
		if sc.ExtID == scID {
			scName = sc.Name
		}
	}
	if cluster.ExtID == r.site.ClusterExtID && scID == r.site.StorageContainerExtID {
		return r.line(step, "kept", fmt.Sprintf("cluster %s and storage container %s kept", cluster.Name, scName))
	}
	if err := r.patch(func(s *Site) { s.ClusterExtID, s.StorageContainerExtID = cluster.ExtID, scID }); err != nil {
		return r.line(step, "failed", err.Error())
	}
	return r.line(step, "ok", fmt.Sprintf("cluster %s, storage container %s", cluster.Name, scName))
}

// live: the stores above ran the settings' change hook, which serves a
// complete site (design sites 4.9).
func (r *setupRun) live() bool {
	if !r.site.Complete() {
		return r.line("live", "failed", "the site lacks its key or its SAML trust")
	}
	return r.line("live", "ok", "the site is complete and served")
}
