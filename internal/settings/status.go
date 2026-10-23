package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/sites"
)

// StatusPart is one row of a site's status card: what Prism Central or
// Keycloak said, and the action that fixes it. Unknown means it could not
// be checked (a slow answer), which says nothing about the setup.
type StatusPart struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
	Unknown bool   `json:"unknown,omitempty"`
}

// ConnectionPart is the connection row: reachable, version, certificate.
type ConnectionPart struct {
	StatusPart
	Version string `json:"version,omitempty"`
	// Trust is how the certificate is trusted: verified, pinned or
	// unverified (a site stored with verification off), or none (http).
	Trust       string    `json:"trust"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	NotAfter    time.Time `json:"not_after,omitzero"`
	certChanged bool
}

// StatusReport is a site's status read from Prism Central and Keycloak at
// the time of the check (sites usability design 4.2); nothing in it comes
// from Portico's own records.
type StatusReport struct {
	Connection     ConnectionPart `json:"connection"`
	ServiceAccount StatusPart     `json:"service_account"`
	SAML           StatusPart     `json:"saml"`
	Defaults       StatusPart     `json:"defaults"`
	keyRefused     bool
}

// KeycloakAdmin is the Keycloak admin client of the document, or nil
// when the realm or its admin client is not set up yet.
func KeycloakAdmin(keycloakURL string, s Settings, log *slog.Logger) *keycloak.Client {
	if keycloakURL == "" || s.Keycloak.Realm == "" || s.Keycloak.AdminClientSecret == "" {
		return nil
	}
	return keycloak.New(keycloakURL, s.Keycloak.Realm, s.Keycloak.AdminClientID, string(s.Keycloak.AdminClientSecret), &http.Client{Timeout: 15 * time.Second}, log)
}

// StatusTimeout bounds one status check.
const StatusTimeout = 30 * time.Second

// SiteStatus checks a site: its certificate (one handshake), the service
// account's key (the clusters list), Portico's identity provider in Prism
// Central with this site's issuer and the Keycloak client, and the stored
// defaults. kc nil skips the Keycloak client.
func SiteStatus(ctx context.Context, kc *keycloak.Client, s Site, log *slog.Logger) StatusReport {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	defer cancel()
	notChecked := StatusPart{Message: "not checked: the connection failed", Unknown: true}
	r := StatusReport{ServiceAccount: notChecked, SAML: notChecked, Defaults: notChecked}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		r.Connection.Message, r.Connection.Action = fmt.Sprintf("the Prism Central URL %q is not valid", s.URL), "Edit"
		return r
	}

	// The certificate.
	switch {
	case u.Scheme != "https":
		r.Connection.Trust = "none"
	case s.TLSPin != "":
		r.Connection.Trust = "pinned"
	case s.TLSVerify:
		r.Connection.Trust = "verified"
	default:
		r.Connection.Trust = "unverified"
	}
	if u.Scheme == "https" {
		info, err := InspectCertificate(ctx, s.URL, []byte(s.CAPEM))
		if err != nil {
			r.Connection.Message = err.Error()
			return r
		}
		r.Connection.Fingerprint, r.Connection.NotAfter = info.Fingerprint, info.NotAfter
		switch {
		case s.TLSPin != "" && info.Fingerprint != s.TLSPin:
			r.Connection.certChanged = true
			r.Connection.Message, r.Connection.Action = "certificate changed: Prism Central presents "+info.Fingerprint, "Confirm the new certificate"
			return r
		case s.TLSPin == "" && s.TLSVerify && !info.Trusted:
			r.Connection.Message, r.Connection.Action = "the certificate is not trusted: "+info.VerifyError, "Pin the current certificate"
			return r
		}
	}

	// The service account's key, which also gives the version.
	tlsCfg, err := proxy.PrismTLSFor(u.Hostname(), s.Trust())
	if err != nil {
		r.Connection.Message, r.Connection.Action = "the CA certificate is not usable: "+err.Error(), "Edit"
		return r
	}
	if s.APIKey == "" {
		r.Connection.OK, r.Connection.Message = true, "reachable"
		r.ServiceAccount = StatusPart{Message: "no service account key is stored", Action: "Finish setup"}
		return r
	}
	pc, err := nutanix.New(s.URL, string(s.APIKey), tlsCfg, log)
	if err != nil {
		r.Connection.Message = err.Error()
		return r
	}
	clusters, err := pc.ListClusters(ctx)
	var apiErr *nutanix.APIError
	switch {
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
		r.Connection.OK, r.Connection.Message = true, "reachable"
		r.keyRefused = true
		r.ServiceAccount = StatusPart{Message: fmt.Sprintf("Prism Central refuses the service account's key (%d)", apiErr.Status), Action: "Repair"}
		return r
	case err != nil:
		r.Connection.Message = prismError(s.URL, err)
		return r
	}
	r.Connection.OK, r.Connection.Version = true, nutanix.PrismCentralVersionOf(clusters)
	r.Connection.Message = "reachable"
	r.ServiceAccount = StatusPart{OK: true, Message: "the key is accepted"}

	r.SAML = samlStatus(ctx, pc, kc, s)
	r.Defaults = defaultsStatus(ctx, pc, clusters, s)
	return r
}

func samlStatus(ctx context.Context, pc *nutanix.Client, kc *keycloak.Client, s Site) StatusPart {
	if s.SAMLIdPExtID == "" && s.SAMLIdPName == "" {
		return StatusPart{Message: "no SAML trust is set up", Action: "Finish setup"}
	}
	var idp *nutanix.SAMLIdP
	if s.SAMLIdPExtID != "" {
		got, err := pc.GetSAMLIdP(ctx, s.SAMLIdPExtID)
		switch {
		case nutanix.IsNotFound(err):
			return StatusPart{Message: "the identity provider " + s.SAMLIdPName + " is missing in Prism Central", Action: "Repair"}
		case err != nil:
			return StatusPart{Message: "the identity provider could not be read: " + errClass(err), Unknown: true}
		}
		idp = got
	} else {
		idps, err := pc.ListSAMLIdPs(ctx)
		if err != nil {
			return StatusPart{Message: "the identity providers could not be read: " + errClass(err), Unknown: true}
		}
		for i := range idps {
			if idps[i].Name == s.SAMLIdPName {
				idp = &idps[i]
			}
		}
		if idp == nil {
			return StatusPart{Message: "the identity provider " + s.SAMLIdPName + " is missing in Prism Central", Action: "Repair"}
		}
	}
	if idp.EntityIssuer != "" && s.SAMLEntityID != "" && idp.EntityIssuer != s.SAMLEntityID {
		return StatusPart{Message: "the identity provider in Prism Central names another site (" + idp.EntityIssuer + ")", Action: "Repair"}
	}
	if kc == nil {
		return StatusPart{OK: true, Message: "identity provider present; Keycloak not checked"}
	}
	// A site set up before its entity id was stored is checked against
	// the issuer Prism Central names; with neither, the client cannot be
	// named, which says nothing about the trust itself.
	entityID := s.SAMLEntityID
	if entityID == "" {
		entityID = idp.EntityIssuer
	}
	if entityID == "" {
		return StatusPart{Message: "the Keycloak client is not known: Repair records it", Action: "Repair", Unknown: true}
	}
	if _, err := kc.FindClient(ctx, entityID); errors.Is(err, keycloak.ErrNotFound) {
		return StatusPart{Message: "the Keycloak client " + entityID + " is missing", Action: "Repair"}
	} else if err != nil {
		return StatusPart{Message: "Keycloak could not be read: " + errClass(err), Unknown: true}
	}
	return StatusPart{OK: true, Message: "identity provider and Keycloak client present"}
}

func defaultsStatus(ctx context.Context, pc *nutanix.Client, clusters []nutanix.Cluster, s Site) StatusPart {
	if s.ClusterExtID == "" || s.StorageContainerExtID == "" {
		return StatusPart{Message: "no default cluster and storage container", Action: "Choose"}
	}
	found := false
	for _, c := range clusters {
		found = found || (c.ExtID == s.ClusterExtID && c.IsAOS)
	}
	if !found {
		return StatusPart{Message: "the default cluster " + s.ClusterExtID + " no longer exists", Action: "Choose"}
	}
	scs, err := pc.ListStorageContainers(ctx)
	if err != nil {
		return StatusPart{Message: "the storage containers could not be read: " + errClass(err), Unknown: true}
	}
	for _, sc := range scs {
		if sc.ExtID == s.StorageContainerExtID {
			return StatusPart{OK: true, Message: "cluster and storage container present"}
		}
	}
	return StatusPart{Message: "the default storage container " + s.StorageContainerExtID + " no longer exists", Action: "Choose"}
}

// MonitorError is the report as the site monitor counts it: a changed
// certificate, a refused key (counted three times in a row), a missing
// trust or Keycloak client (at once), an unreachable Prism Central, or
// nil. Vanished defaults are a warning on the page only.
func (r StatusReport) MonitorError() error {
	switch {
	case r.Connection.certChanged:
		return &sites.CertChangedError{Fingerprint: r.Connection.Fingerprint}
	case !r.Connection.OK:
		return errors.New(r.Connection.Message)
	case r.keyRefused:
		return &sites.IncompleteError{Reason: r.ServiceAccount.Message}
	case !r.SAML.OK && !r.SAML.Unknown:
		return &sites.IncompleteError{Reason: r.SAML.Message, Immediate: true}
	}
	return nil
}
