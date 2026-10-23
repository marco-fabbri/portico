package settings

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
	"github.com/marco-fabbri/portico/internal/sites"
)

// statusEnv is a set-up site: its identity provider in Prism Central and
// its SAML client in Keycloak.
func statusEnv(t *testing.T) (*settingstest.PC, *settingstest.KC, *keycloak.Client, Site) {
	t.Helper()
	pc, kc := settingstest.NewPC(t), settingstest.NewKC(t)
	kc.Realms["msp"] = map[string]any{"realm": "msp"}
	entity := "https://prism.example.test/api/iam/authn/site-a"
	kc.Clients[entity] = map[string]any{"id": "c-1", "clientId": entity}
	pc.IdPs = append(pc.IdPs, map[string]any{"extId": "idp-1", "name": "portico-keycloak", "entityIssuer": entity})
	kcc := keycloak.New(kc.Srv.URL, "msp", "portico-admin", kc.RealmSecret, &http.Client{Timeout: 5 * time.Second}, nil)
	site := Site{ID: "site-a", Code: "SITE-A", URL: pc.Srv.URL, APIKey: Secret(pc.Key), SAMLIdPName: "portico-keycloak", SAMLIdPExtID: "idp-1",
		SAMLEntityID: entity, ClusterExtID: "cl-1", StorageContainerExtID: "sc-1"}
	return pc, kc, kcc, site
}

func TestStatusAllOK(t *testing.T) {
	_, _, kcc, site := statusEnv(t)
	r := SiteStatus(context.Background(), kcc, site, nil)
	if !r.Connection.OK || !r.ServiceAccount.OK || !r.SAML.OK || !r.Defaults.OK || r.MonitorError() != nil {
		t.Fatalf("report = %+v", r)
	}
}

func TestStatusTrustMissing(t *testing.T) {
	pc, _, kcc, site := statusEnv(t)
	pc.IdPs = nil
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.SAML.OK || r.SAML.Action != "Repair" || !strings.Contains(r.SAML.Message, "missing") {
		t.Fatalf("saml = %+v", r.SAML)
	}
	var inc *sites.IncompleteError
	if err := r.MonitorError(); !errors.As(err, &inc) || !inc.Immediate {
		t.Fatalf("monitor error = %v", err)
	}
}

func TestStatusKeycloakClientMissing(t *testing.T) {
	_, kc, kcc, site := statusEnv(t)
	delete(kc.Clients, site.SAMLEntityID)
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.SAML.OK || r.SAML.Action != "Repair" || !strings.Contains(r.SAML.Message, "Keycloak") {
		t.Fatalf("saml = %+v", r.SAML)
	}
}

func TestStatusOtherEntityIssuer(t *testing.T) {
	pc, _, kcc, site := statusEnv(t)
	pc.IdPs[0]["entityIssuer"] = "https://prism.example.test/api/iam/authn/site-b"
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.SAML.OK {
		t.Fatalf("an identity provider of another site counted as this one's: %+v", r.SAML)
	}
}

func TestStatusDefaultsGone(t *testing.T) {
	_, _, kcc, site := statusEnv(t)
	site.StorageContainerExtID = "sc-gone"
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.Defaults.OK || !strings.Contains(r.Defaults.Message, "sc-gone") {
		t.Fatalf("defaults = %+v", r.Defaults)
	}
	if r.MonitorError() != nil {
		t.Fatal("vanished defaults made the site incomplete: they are a warning only")
	}
}

func TestStatusNeverFromRow(t *testing.T) {
	pc, _, kcc, site := statusEnv(t)
	pc.IdPs = nil
	site.SAMLIdPExtID = "" // a site set up before: only the stored name
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.SAML.OK {
		t.Fatal("the stored provider name made the SAML trust ok")
	}
}

func TestStatusKeyRefused(t *testing.T) {
	_, _, kcc, site := statusEnv(t)
	site.APIKey = "revoked"
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.ServiceAccount.OK || !strings.Contains(r.ServiceAccount.Message, "401") {
		t.Fatalf("service account = %+v", r.ServiceAccount)
	}
	var inc *sites.IncompleteError
	if err := r.MonitorError(); !errors.As(err, &inc) || inc.Immediate {
		t.Fatalf("monitor error = %v, want a non-immediate IncompleteError", err)
	}
}

// A site set up before its entity id was stored (site-1 of an upgraded
// appliance) is checked against the issuer Prism Central names, and is
// never made incomplete for what Portico did not record.
func TestStatusEntityIDFromPrismCentral(t *testing.T) {
	_, kc, kcc, site := statusEnv(t)
	site.SAMLEntityID = ""
	if r := SiteStatus(context.Background(), kcc, site, nil); !r.SAML.OK {
		t.Fatalf("issuer from Prism Central: %+v", r.SAML)
	}
	delete(kc.Clients, "https://prism.example.test/api/iam/authn/site-a")
	if r := SiteStatus(context.Background(), kcc, site, nil); r.SAML.OK || r.SAML.Action != "Repair" {
		t.Fatalf("its client gone: %+v", r.SAML)
	}
}

func TestStatusEntityIDUnknownIsNotIncomplete(t *testing.T) {
	pc, _, kcc, site := statusEnv(t)
	site.SAMLEntityID = ""
	delete(pc.IdPs[0], "entityIssuer")
	r := SiteStatus(context.Background(), kcc, site, nil)
	if r.SAML.OK || !r.SAML.Unknown || r.SAML.Action != "Repair" {
		t.Fatalf("saml = %+v", r.SAML)
	}
	if err := r.MonitorError(); err != nil {
		t.Fatalf("an unrecorded entity id made the site incomplete: %v", err)
	}
}
