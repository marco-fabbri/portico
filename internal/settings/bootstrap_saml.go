package settings

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
)

const defaultIdPName = "portico-keycloak"

// perSiteEntityID chooses how several sites behind one public name meet
// Keycloak (design sites §4.5). Prism Central derives its SAML entity id
// from the IdP's redirect URL, the same for every site: false keeps that
// shared id and one Keycloak client for all sites (variant B); true sets
// a per-site entityIssuer and one client per site (variant A). Decision
// D3 of write-up 20: pc.7.6.0.6 accepts the entityIssuer, publishes it
// in the SP metadata and logs in with a client of that name, so each
// site gets its own client. A site created before (site-1 of an upgraded
// appliance) keeps the shared default until its trust is recreated.
var perSiteEntityID = true

// samlDescriptor reads the realm's SAML descriptor and checks that it
// names the public identity host; the message says what is wrong.
func samlDescriptor(ctx context.Context, kc *keycloak.Client, s Settings) ([]byte, string) {
	if kc == nil {
		return nil, "the Keycloak realm with its admin client must be set first"
	}
	descriptor, err := kc.SAMLDescriptor(ctx)
	if err != nil {
		return nil, "Keycloak did not answer: " + errClass(err)
	}
	wantIssuer := "https://" + s.IDHost + "/realms/" + s.Keycloak.Realm
	var ed struct {
		EntityID string `xml:"entityID,attr"`
	}
	if err := xml.Unmarshal(descriptor, &ed); err != nil || ed.EntityID == "" {
		return nil, "the identity provider's descriptor is not readable"
	}
	if ed.EntityID != wantIssuer {
		return nil, fmt.Sprintf("the identity provider's descriptor names %s, expected %s: check Keycloak's hostname setting", ed.EntityID, wantIssuer)
	}
	return descriptor, ""
}

// siteEntityIssuer is the SAML service-provider name Prism Central uses
// for site (D3, write-up 20), or "" when it keeps its own default.
func siteEntityIssuer(s Settings, siteID string) string {
	if !perSiteEntityID {
		return ""
	}
	return "https://" + s.PortalHost + "/api/iam/authn/" + siteID
}

// ownIdP picks Portico's identity provider among Prism Central's: the one
// with the stored id, or the one with Portico's name whose issuer is this
// site's (or Prism Central's default). Any other is foreign: Portico does
// not adopt an existing provider, and names it.
func ownIdP(idps []nutanix.SAMLIdP, s Settings, site Site, name string) (own *nutanix.SAMLIdP, foreign string) {
	issuer := siteEntityIssuer(s, site.ID)
	for i := range idps {
		switch {
		case site.SAMLIdPExtID != "" && idps[i].ExtID == site.SAMLIdPExtID,
			idps[i].Name == name && (idps[i].EntityIssuer == "" || idps[i].EntityIssuer == issuer):
			own = &idps[i]
		default:
			return nil, idps[i].Name
		}
	}
	return own, ""
}

// samlRefusal is a Prism Central error of the SAML step; without one-time
// credentials a 403 means the service account may not manage providers.
func samlRefusal(target string, err error, creds bool) string {
	var apiErr *nutanix.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && !creds {
		return "Prism Central refused the service account (403): give the credentials of an account that can manage identity providers"
	}
	return credentialRefusal(target, err)
}

// samlOutcome is what the SAML trust step leaves.
type samlOutcome struct {
	IdPExtID, EntityID, ACSURL string
	Created                    []string
}

// samlTrust creates Portico's identity provider in Prism Central unless
// own is it, then the Keycloak SAML client its SP metadata describes,
// with the email and groups mappers. The message says what failed; what
// was created stays, and a run again finds it.
func (b *Bootstrapper) samlTrust(ctx context.Context, pc *nutanix.Client, kc *keycloak.Client, s Settings, siteID, name string, own *nutanix.SAMLIdP, descriptor []byte, creds bool) (samlOutcome, string) {
	site, _ := s.Site(siteID)
	out := samlOutcome{Created: []string{}}
	idp := own
	if idp == nil {
		spec := nutanix.SAMLIdPSpec{Name: name, MetadataXML: descriptor, RedirectURL: "https://" + s.PortalHost, EntityIssuer: siteEntityIssuer(s, siteID)}
		created, err := pc.CreateSAMLIdP(ctx, spec)
		if err != nil {
			return out, samlRefusal(site.URL, err, creds)
		}
		idp = created
		b.log.Info("bootstrap saml: identity provider created", "name", name, "ext_id", idp.ExtID)
		out.Created = append(out.Created, "identity provider "+name)
	}
	out.IdPExtID = idp.ExtID
	entityID, acs, err := pc.SPMetadata(ctx, idp.ExtID)
	if err != nil {
		return out, samlRefusal(site.URL, err, creds)
	}
	out.EntityID, out.ACSURL = entityID, acs

	// Keycloak: the SAML client Prism Central's SP metadata describes
	// (write-up 04; no single logout, write-up 13). No admin URL: Keycloak
	// sends its SAML backchannel logout there, to the public Prism Central
	// name, which its container cannot resolve and Portico would refuse
	// without a session (write-up 24); Portico's logout expires Prism
	// Central's cookies instead.
	rep := map[string]any{"clientId": entityID, "protocol": "saml", "enabled": true, "name": "Prism Central",
		"redirectUris": []string{acs}, "baseUrl": acs, "adminUrl": "", "frontchannelLogout": false,
		"attributes": map[string]string{
			"saml.assertion.signature": "true", "saml.server.signature": "true", "saml.client.signature": "false",
			"saml.force.post.binding": "true", "saml.authnstatement": "true", "saml_name_id_format": "email",
			"saml_force_name_id_format": "true", "saml.signature.algorithm": "RSA_SHA256",
			"saml_assertion_consumer_url_post": acs, "saml_single_logout_service_url_post": "", "saml_single_logout_service_url_redirect": "",
		}}
	kcErr := func(err error) string {
		if strings.Contains(err.Error(), "token") {
			return fmt.Sprintf("Keycloak refused the admin client %q in realm %q", s.Keycloak.AdminClientID, s.Keycloak.Realm)
		}
		return "Keycloak answered an error: " + strings.TrimPrefix(err.Error(), "keycloak: ")
	}
	clientUUID, created, err := kc.EnsureClient(ctx, rep)
	if err != nil {
		return out, kcErr(err)
	}
	if created {
		b.log.Info("bootstrap saml: keycloak client created", "client", entityID)
		out.Created = append(out.Created, "keycloak client "+entityID)
	}
	mappers := []map[string]any{
		{"name": "email", "protocol": "saml", "protocolMapper": "saml-user-property-mapper", "config": map[string]string{"user.attribute": "email", "attribute.name": "email", "attribute.nameformat": "Basic"}},
		{"name": "groups", "protocol": "saml", "protocolMapper": "saml-group-membership-mapper", "config": map[string]string{"attribute.name": "groups", "attribute.nameformat": "Basic", "single": "true", "full.path": "false"}},
	}
	added, err := kc.EnsureProtocolMappers(ctx, clientUUID, mappers)
	if err != nil {
		return out, kcErr(err)
	}
	if len(added) > 0 {
		b.log.Info("bootstrap saml: mappers added", "client", entityID, "mappers", added)
		out.Created = append(out.Created, "mappers "+strings.Join(added, ", "))
	}
	return out, ""
}
