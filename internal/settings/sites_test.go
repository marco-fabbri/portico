package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A document written by a single-Prism Central release becomes site
// site-1 at load, with its sealed key still opening (design sites §4.7).
func TestLegacyDocumentBecomesSite1(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sealer := sealerFor(t, 1)
	sealedKey, err := sealer.Seal("legacy-key")
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"portal_host": "prism.example.test", "id_host": "id.example.test",
		"prism_central": map[string]any{"url": "https://10.0.0.5:9440", "tls_verify": false, "ca_pem": "", "api_key": sealedKey, "saml_idp_name": "portico-keycloak"},
		"tenants":       map[string]any{"project_prefix": "portico", "cluster_ext_id": "cl-1", "storage_container_ext_id": "sc-1"},
	}
	raw, _ := json.Marshal(legacy)
	if err := st.SetSetting(ctx, SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	got, err := NewService(st, sealer).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Sites) != 1 {
		t.Fatalf("sites = %+v", got.Sites)
	}
	s := got.Sites[0]
	if s.ID != LegacySiteID || s.Code != LegacySiteCode || s.Name != "10.0.0.5" || s.URL != "https://10.0.0.5:9440" || s.TLSVerify ||
		s.APIKey != "legacy-key" || s.SAMLIdPName != "portico-keycloak" || s.ClusterExtID != "cl-1" || s.StorageContainerExtID != "sc-1" {
		t.Fatalf("site-1 = %+v", s)
	}
	// Saved again, the document has no legacy block any more.
	svc := NewService(st, sealer)
	if err := svc.Set(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw, _ = st.GetSetting(ctx, SettingKey)
	if strings.Contains(string(raw), `"prism_central"`) || strings.Contains(string(raw), "legacy-key") {
		t.Fatalf("stored again = %s", raw)
	}
}

func TestMissingListsSiteFields(t *testing.T) {
	s := sample()
	s.Sites = nil
	if m := s.Missing(); len(m) != 1 || m[0] != "sites" {
		t.Fatalf("no site: missing = %v", m)
	}
	s.Sites = []Site{{ID: "mi1", Code: "AZ-IT-MI1"}}
	if m := s.Missing(); len(m) != 1 || m[0] != "sites" {
		t.Fatalf("no complete site: missing = %v", m)
	}
	if inc := s.IncompleteSites(); len(inc) != 1 || inc[0] != "mi1 (url, api_key, saml_idp_name)" {
		t.Fatalf("incomplete = %v", inc)
	}
}

func TestValidateSiteShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*Settings)
		want string
	}{
		{"bad id", func(s *Settings) { s.Sites[0].ID = "Bad ID" }, "sites[0].id"},
		{"bad code", func(s *Settings) { s.Sites[0].Code = "az-it-mi1" }, "sites[0].code"},
		{"duplicate id", func(s *Settings) { s.Sites = append(s.Sites, Site{ID: s.Sites[0].ID, Code: "OTHER"}) }, "duplicate site id"},
		{"duplicate code", func(s *Settings) { s.Sites = append(s.Sites, Site{ID: "other", Code: s.Sites[0].Code}) }, "duplicate site code"},
		{"unknown passthrough", func(s *Settings) { s.PassthroughSite = "nope" }, "passthrough_site"},
		{"url with credentials", func(s *Settings) { s.Sites[0].URL = "https://admin:pw@pc:9440" }, "must not carry credentials"},
	} {
		s := sample()
		tc.mod(&s)
		err := s.ValidateFormat()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: the error echoes the password", tc.name)
		}
	}
	if err := sample().ValidateFormat(); err != nil {
		t.Fatalf("sample: %v", err)
	}
}

func TestUpdateKeepsSiteKeyByID(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	in := sample()
	in.Sites = append(in.Sites, Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: "https://pc2:9440", APIKey: "nyc-key", SAMLIdPName: "portico-keycloak"})
	if err := svc.Set(ctx, in); err != nil {
		t.Fatal(err)
	}
	// The same sites in the other order, keys empty: both kept by id.
	up := sample()
	up.Sites = []Site{{ID: "nyc1", Code: "AZ-US-NYC1", URL: "https://pc2:9440", SAMLIdPName: "portico-keycloak"}, up.Sites[0]}
	up.Sites[1].APIKey = ""
	if err := svc.Update(ctx, up); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx)
	keys := map[string]Secret{}
	for _, s := range got.Sites {
		keys[s.ID] = s.APIKey
	}
	if keys["nyc1"] != "nyc-key" || keys[sample().Sites[0].ID] != sample().Sites[0].APIKey {
		t.Fatalf("keys after Update = %v", keys)
	}
}

func TestSiteLookupAndOrder(t *testing.T) {
	s := sample()
	done := func(id, code string, order int) Site {
		return Site{ID: id, Code: code, Order: order, URL: "https://" + id + ":9440", APIKey: "k", SAMLIdPName: "idp"}
	}
	s.Sites = []Site{done("b", "B", 2), done("a", "A", 1), done("c", "C", 1)}
	got := s.OrderedSites()
	if got[0].ID != "a" || got[1].ID != "c" || got[2].ID != "b" {
		t.Fatalf("ordered = %+v", got)
	}
	if site, ok := s.Site("c"); !ok || site.Code != "C" {
		t.Fatalf("Site(c) = %+v %v", site, ok)
	}
	if first, ok := s.PassthroughSiteOrFirst(); !ok || first.ID != "a" {
		t.Fatalf("passthrough default = %+v", first)
	}
	s.PassthroughSite = "b"
	if p, _ := s.PassthroughSiteOrFirst(); p.ID != "b" {
		t.Fatalf("passthrough = %+v", p)
	}
	if SiteIDFromCode("AZ-IT-MI1") != "az-it-mi1" || !ValidSiteID("az-it-mi1") || ValidSiteID("-x") || !ValidSiteCode("AZ-IT-MI1") || ValidSiteCode("A") {
		t.Fatal("site id/code helpers")
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"pc.7.6.0.6", true}, {"7.6", true}, {"pc.7.7", true}, {"pc.8.0.1", true}, {"pc.7.5.1", false},
		{"pc.2024.3", false}, {"pc.2024.3.1", false}, {"", false}, {"garbage", false},
	} {
		if got := VersionAtLeast(tc.v, 7, 6); got != tc.want {
			t.Errorf("VersionAtLeast(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// fakePCVersion answers the clusters list with a Prism Central entry
// carrying the given build version.
func fakePCVersion(t *testing.T, key, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Ntnx-Api-Key") != key {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/config/clusters"):
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"extId": "cl-1", "name": "NTNX-01", "config": map[string]any{"clusterFunction": []string{"AOS"}, "buildInfo": map[string]any{"version": "7.6"}}},
				{"extId": "pc-1", "name": "PC", "config": map[string]any{"clusterFunction": []string{"PRISM_CENTRAL"}, "buildInfo": map[string]any{"version": version}}},
			}})
		case strings.HasSuffix(r.URL.Path, "/config/storage-containers"):
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckerRefusesOldPrismCentral(t *testing.T) {
	old := fakePCVersion(t, "k", "pc.2024.3")
	res := NewChecker(nil).PrismCentral(context.Background(), Site{ID: "s", URL: old.URL}, "k")
	if res.OK || !strings.Contains(res.Message, "pc.2024.3") || !strings.Contains(res.Message, "pc.7.6") || res.Version != "pc.2024.3" {
		t.Fatalf("old: %+v", res)
	}
	cur := fakePCVersion(t, "k", "pc.7.6.0.6")
	res = NewChecker(nil).PrismCentral(context.Background(), Site{ID: "s", URL: cur.URL}, "k")
	if !res.OK || res.Version != "pc.7.6.0.6" {
		t.Fatalf("current: %+v", res)
	}
}

func TestBootstrapPrismUnknownSite(t *testing.T) {
	pc := newFakePCAdmin(t)
	s := sample()
	_, _, err := NewBootstrapper(nil, nil).PrismCentral(context.Background(), s, "nope", prismInput(pc.srv.URL))
	var se *StateError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if len(pc.posts) != 0 {
		t.Fatalf("Prism Central was called: %v", pc.posts)
	}
}

// Two sites, each with its own Prism Central, trust the same realm. With
// the shared client (variant B) the second bootstrap only ensures the
// Keycloak client; with per-site issuers (variant A) each site gets its
// own client named by its issuer.
func TestBootstrapSAMLPerSite(t *testing.T) {
	for _, perSite := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared client", true: "per-site issuer"}[perSite], func(t *testing.T) {
			old := perSiteEntityID
			perSiteEntityID = perSite
			t.Cleanup(func() { perSiteEntityID = old })
			pc1, kc, s := samlEnv(t)
			pc2 := newFakePCAdmin(t)
			s.Sites = append(s.Sites, Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: pc2.srv.URL, APIKey: Secret(pc2.issuedKey)})
			b := NewBootstrapper(nil, nil)
			res1, out, err := b.SAML(context.Background(), kc.srv.URL, s, s.Sites[0].ID, SAMLInput{})
			if err != nil || !res1.OK {
				t.Fatalf("site 1: %+v %v", res1, err)
			}
			res2, out, err := b.SAML(context.Background(), kc.srv.URL, out, "nyc1", SAMLInput{})
			if err != nil || !res2.OK {
				t.Fatalf("site 2: %+v %v", res2, err)
			}
			nyc, _ := out.Site("nyc1")
			first, _ := out.Site(s.Sites[0].ID)
			if nyc.SAMLIdPName != "portico-keycloak" || nyc.SAMLEntityID != res2.EntityID || first.SAMLEntityID != res1.EntityID {
				t.Fatalf("sites after = %+v / %+v", first, nyc)
			}
			if pc1.idpPost == nil || pc2.idpPost == nil {
				t.Fatal("each Prism Central must get its identity provider")
			}
			if perSite {
				if pc2.idpPost["entityIssuer"] != "https://prism.example.test/api/iam/authn/nyc1" || res1.EntityID == res2.EntityID {
					t.Fatalf("per-site issuers: post=%v e1=%s e2=%s", pc2.idpPost, res1.EntityID, res2.EntityID)
				}
				if kc.client(res1.EntityID) == nil || kc.client(res2.EntityID) == nil {
					t.Fatal("one Keycloak client per site")
				}
			} else {
				if _, set := pc2.idpPost["entityIssuer"]; set || res1.EntityID != res2.EntityID || kc.client(res1.EntityID) == nil {
					t.Fatalf("shared client: post=%v e1=%s e2=%s", pc2.idpPost, res1.EntityID, res2.EntityID)
				}
			}
		})
	}
}

// withSite adds an empty site "site-a" for the bootstraps that need one.
func withSite(s Settings) Settings {
	s.Sites = append(s.Sites, Site{ID: "site-a", Code: "SITE-A"})
	return s
}

// Review I1: an incomplete site does not make the document incomplete
// while another site is complete; it is listed apart and not served.
func TestIncompleteSiteDoesNotBlockTheOthers(t *testing.T) {
	s := sample()
	s.Sites = append(s.Sites, Site{ID: "nyc1", Code: "AZ-US-NYC1", URL: "https://pc2:9440"})
	if m := s.Missing(); len(m) != 0 {
		t.Fatalf("missing with one complete site = %v", m)
	}
	if served := s.ServedSites(); len(served) != 1 || served[0].ID != "site-a" {
		t.Fatalf("served = %+v", served)
	}
	if inc := s.IncompleteSites(); len(inc) != 1 || inc[0] != "nyc1 (api_key, saml_idp_name)" {
		t.Fatalf("incomplete = %v", inc)
	}
	s.Sites = s.Sites[1:]
	if m := s.Missing(); len(m) != 1 || m[0] != "sites" {
		t.Fatalf("missing with no complete site = %v", m)
	}
	s.PassthroughSite = "nyc1"
	if p, ok := s.PassthroughSiteOrFirst(); ok {
		t.Fatalf("an incomplete site served the pass-through: %+v", p)
	}
}

func TestSiteTrustPinWins(t *testing.T) {
	s := Site{URL: "https://pc.example:9440", TLSPin: "AB:CD", TLSVerify: false, CAPEM: "x"}
	tr := s.Trust()
	if tr.Pin != "AB:CD" || string(tr.CAPEM) != "x" || tr.Verify {
		t.Fatalf("trust = %+v", tr)
	}
}

func TestSecretFieldsUnchangedByPin(t *testing.T) {
	doc := Settings{Sites: []Site{{ID: "site-1", Code: "SITE-1", TLSPin: "AB:CD"}}}
	for _, f := range secretFields(&doc) {
		if strings.Contains(f.name, "pin") {
			t.Fatalf("the pin is sealed: %s", f.name)
		}
	}
}
