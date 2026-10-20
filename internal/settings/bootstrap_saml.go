package settings

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
)

// SAMLInput is the SAML trust bootstrap request: the identity provider
// name in Prism Central and, optionally, one-time credentials for the
// case where the service account may not manage identity providers.
type SAMLInput struct {
	Name     string // defaults to "portico-keycloak"
	Username string
	Password string
}

// SAMLResult is what the bootstrap reports.
type SAMLResult struct {
	OK               bool     `json:"ok"`
	Message          string   `json:"message"`
	Name             string   `json:"name"` // the identity-provider name used (input or default)
	IdPExtID         string   `json:"idp_ext_id,omitempty"`
	EntityID         string   `json:"entity_id,omitempty"`
	ACSURL           string   `json:"acs_url,omitempty"`
	KeycloakClientID string   `json:"keycloak_client_id,omitempty"`
	UsedCredentials  bool     `json:"used_credentials"`
	Created          []string `json:"created"`
	Stored           bool     `json:"stored"`
}

const defaultIdPName = "portico-keycloak"

// perSiteEntityID chooses how several sites behind one public name meet
// Keycloak (design sites §4.5). Prism Central derives its SAML entity id
// from the IdP's redirect URL, the same for every site: false keeps that
// shared id and one Keycloak client for all sites (variant B); true sets
// a per-site entityIssuer and one client per site (variant A). Decision
// D3 of write-up 20; variant B is the behaviour measured until then.
var perSiteEntityID = false

// SAML establishes the trust between Keycloak and Prism Central (design
// §4.4): the realm's SAML descriptor becomes the identity provider in
// Prism Central (with the public name as redirect URL), and the SP
// metadata Prism Central publishes for it becomes a SAML client in
// Keycloak. Prism Central holds one SAML identity provider: another one
// is a *StateError, never adopted.
func (b *Bootstrapper) SAML(ctx context.Context, keycloakURL string, s Settings, siteID string, in SAMLInput) (SAMLResult, Settings, error) {
	site, ok := s.Site(siteID)
	if !ok {
		return SAMLResult{}, s, &StateError{Msg: fmt.Sprintf("no site with id %q", siteID)}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = defaultIdPName
	}
	creds := in.Username != "" && in.Password != ""
	if strings.TrimSpace(s.PrismHost) == "" || strings.TrimSpace(s.IDHost) == "" || s.Keycloak.Realm == "" || s.Keycloak.AdminClientSecret == "" || site.URL == "" || (site.APIKey == "" && !creds) {
		return SAMLResult{}, s, &StateError{Msg: "the public names, the Keycloak realm with its admin client and the site's Prism Central connection must be set first"}
	}
	if !reServiceAccount.MatchString(name) {
		return SAMLResult{}, s, &StateError{Msg: "the identity provider name must be lower-case letters, digits, dots, dashes and underscores"}
	}
	res := SAMLResult{Created: []string{}, UsedCredentials: creds, Name: name}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// The realm descriptor, checked against the public identity name.
	kc := keycloak.New(keycloakURL, s.Keycloak.Realm, s.Keycloak.AdminClientID, string(s.Keycloak.AdminClientSecret), &http.Client{Timeout: 15 * time.Second}, b.log)
	descriptor, err := kc.SAMLDescriptor(ctx)
	if err != nil {
		res.Message = "Keycloak did not answer: " + errClass(err)
		return res, s, nil
	}
	wantIssuer := "https://" + s.IDHost + "/realms/" + s.Keycloak.Realm
	var ed struct {
		EntityID string `xml:"entityID,attr"`
	}
	if err := xml.Unmarshal(descriptor, &ed); err != nil || ed.EntityID == "" {
		res.Message = "the identity provider's descriptor is not readable"
		return res, s, nil
	}
	if ed.EntityID != wantIssuer {
		res.Message = fmt.Sprintf("the identity provider's descriptor names %s, expected %s: check Keycloak's hostname setting", ed.EntityID, wantIssuer)
		return res, s, nil
	}

	// Prism Central: the service account, or the one-time credentials.
	u, err := url.Parse(site.URL)
	if err != nil || u.Host == "" {
		return SAMLResult{}, s, &StateError{Msg: fmt.Sprintf("the Prism Central URL %q is not valid", site.URL)}
	}
	tlsCfg, err := proxy.PrismTLSPEM(u.Hostname(), []byte(site.CAPEM), site.TLSVerify)
	if err != nil {
		return SAMLResult{}, s, &StateError{Msg: "the CA certificate is not usable: " + err.Error()}
	}
	var pc *nutanix.Client
	if creds {
		pc, err = nutanix.NewWithPassword(site.URL, in.Username, in.Password, tlsCfg, b.log)
	} else {
		pc, err = nutanix.New(site.URL, string(site.APIKey), tlsCfg, b.log)
	}
	if err != nil {
		return SAMLResult{}, s, &StateError{Msg: err.Error()}
	}
	remote := func(err error) (SAMLResult, Settings, error) {
		var apiErr *nutanix.APIError
		res.OK = false
		switch {
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && !creds:
			res.Message = "Prism Central refused the service account (403): give the credentials of an account that can manage identity providers"
		case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
			res.Message = fmt.Sprintf("Prism Central refused the credentials (%d)", apiErr.Status)
		default:
			res.Message = prismError(site.URL, err)
		}
		return res, s, nil
	}
	idps, err := pc.ListSAMLIdPs(ctx)
	if err != nil {
		return remote(err)
	}
	var idp *nutanix.SAMLIdP
	for i := range idps {
		if idps[i].Name == name {
			idp = &idps[i]
			continue
		}
		return SAMLResult{}, s, &StateError{Msg: fmt.Sprintf("an identity provider named %s already exists: Portico does not adopt an existing one", idps[i].Name)}
	}
	if idp == nil {
		spec := nutanix.SAMLIdPSpec{Name: name, MetadataXML: descriptor, RedirectURL: "https://" + s.PrismHost}
		if perSiteEntityID {
			spec.EntityIssuer = "https://" + s.PrismHost + "/api/iam/authn/" + siteID
		}
		idp, err = pc.CreateSAMLIdP(ctx, spec)
		if err != nil {
			return remote(err)
		}
		b.log.Info("bootstrap saml: identity provider created", "name", name, "ext_id", idp.ExtID)
		res.Created = append(res.Created, "identity provider "+name)
	}
	res.IdPExtID = idp.ExtID
	entityID, acs, err := pc.SPMetadata(ctx, idp.ExtID)
	if err != nil {
		return remote(err)
	}
	res.EntityID, res.ACSURL, res.KeycloakClientID = entityID, acs, entityID

	// Keycloak: the SAML client Prism Central's SP metadata describes
	// (write-up 04; no single logout, write-up 13).
	rep := map[string]any{"clientId": entityID, "protocol": "saml", "enabled": true, "name": "Prism Central",
		"redirectUris": []string{acs}, "baseUrl": acs, "adminUrl": acs, "frontchannelLogout": false,
		"attributes": map[string]string{
			"saml.assertion.signature": "true", "saml.server.signature": "true", "saml.client.signature": "false",
			"saml.force.post.binding": "true", "saml.authnstatement": "true", "saml_name_id_format": "email",
			"saml_force_name_id_format": "true", "saml.signature.algorithm": "RSA_SHA256",
			"saml_assertion_consumer_url_post": acs, "saml_single_logout_service_url_post": "", "saml_single_logout_service_url_redirect": "",
		}}
	kcErr := func(err error) (SAMLResult, Settings, error) {
		res.OK = false
		if strings.Contains(err.Error(), "token") {
			res.Message = fmt.Sprintf("Keycloak refused the admin client %q in realm %q", s.Keycloak.AdminClientID, s.Keycloak.Realm)
		} else {
			res.Message = "Keycloak answered an error: " + strings.TrimPrefix(err.Error(), "keycloak: ")
		}
		return res, s, nil
	}
	clientUUID, created, err := kc.EnsureClient(ctx, rep)
	if err != nil {
		return kcErr(err)
	}
	if created {
		b.log.Info("bootstrap saml: keycloak client created", "client", entityID)
		res.Created = append(res.Created, "keycloak client "+entityID)
	}
	mappers := []map[string]any{
		{"name": "email", "protocol": "saml", "protocolMapper": "saml-user-property-mapper", "config": map[string]string{"user.attribute": "email", "attribute.name": "email", "attribute.nameformat": "Basic"}},
		{"name": "groups", "protocol": "saml", "protocolMapper": "saml-group-membership-mapper", "config": map[string]string{"attribute.name": "groups", "attribute.nameformat": "Basic", "single": "true", "full.path": "false"}},
	}
	added, err := kc.EnsureProtocolMappers(ctx, clientUUID, mappers)
	if err != nil {
		return kcErr(err)
	}
	if len(added) > 0 {
		b.log.Info("bootstrap saml: mappers added", "client", entityID, "mappers", added)
		res.Created = append(res.Created, "mappers "+strings.Join(added, ", "))
	}

	patch := func(d *Settings) {
		if i := d.siteIndex(siteID); i >= 0 {
			d.Sites[i].SAMLIdPName, d.Sites[i].SAMLEntityID = name, entityID
		}
	}
	out := s
	out.Sites = append([]Site(nil), s.Sites...)
	patch(&out)
	if len(res.Created) == 0 {
		res.Message = fmt.Sprintf("the SAML trust is in place: identity provider %s and Keycloak client %s", name, entityID)
	} else {
		res.Message = "SAML trust done: created " + strings.Join(res.Created, ", ")
	}
	stored, err := b.store(ctx, patch)
	if err != nil {
		// The trust exists on both sides; a retry finds it and stores the name.
		res.Message = "SAML trust done but the settings were not stored: " + err.Error()
		return res, s, nil
	}
	res.OK, res.Stored = true, stored
	return res, out, nil
}
