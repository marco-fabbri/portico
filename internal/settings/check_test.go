package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePC answers the two list calls the Prism Central check makes.
func fakePC(t *testing.T, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Ntnx-Api-Key") != key {
			w.WriteHeader(401)
			w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/config/clusters"):
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"extId": "cl-1", "name": "NTNX-01", "config": map[string]any{"clusterFunction": []string{"AOS"}}},
				{"extId": "pc-1", "name": "Unnamed", "config": map[string]any{"clusterFunction": []string{"PRISM_CENTRAL"}}},
			}})
		case strings.HasSuffix(r.URL.Path, "/config/storage-containers"):
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"containerExtId": "sc-1", "name": "default-container-1"},
				{"containerExtId": "sc-2", "name": "SelfServiceContainer"},
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckPrismCentralListsClustersAndContainers(t *testing.T) {
	srv := fakePC(t, "good-key")
	s := sample()
	s.PrismCentral.URL, s.PrismCentral.TLSVerify = srv.URL, false
	res := NewChecker(nil).PrismCentral(context.Background(), s, "good-key")
	if !res.OK || len(res.Clusters) != 1 || res.Clusters[0].ExtID != "cl-1" || res.Clusters[0].Name != "NTNX-01" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Containers) != 1 || res.Containers[0].ExtID != "sc-1" {
		t.Fatalf("containers = %+v (system containers must be left out)", res.Containers)
	}
	if !strings.Contains(res.Message, "1 cluster") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestCheckPrismCentralWrongKey(t *testing.T) {
	srv := fakePC(t, "good-key")
	s := sample()
	s.PrismCentral.URL, s.PrismCentral.TLSVerify = srv.URL, false
	res := NewChecker(nil).PrismCentral(context.Background(), s, "wrong-key")
	if res.OK || !strings.Contains(res.Message, "API key") || strings.Contains(res.Message, "wrong-key") {
		t.Fatalf("result = %+v", res)
	}
}

func TestCheckPrismCentralUnreachable(t *testing.T) {
	srv := fakePC(t, "k")
	url := srv.URL
	srv.Close()
	s := sample()
	s.PrismCentral.URL, s.PrismCentral.TLSVerify = url, false
	res := NewChecker(nil).PrismCentral(context.Background(), s, "k")
	if res.OK || !strings.Contains(res.Message, url) {
		t.Fatalf("result = %+v", res)
	}
}

func TestCheckKeycloakTokenAndRealm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			r.ParseForm()
			if r.Form.Get("client_secret") != "good-secret" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","expires_in":60}`))
		case strings.HasSuffix(r.URL.Path, "/admin/realms/portico"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"realm":"portico","loginTheme":"portico"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := sample()
	res := NewChecker(nil).Keycloak(context.Background(), srv.URL, s, "good-secret")
	if !res.OK || !strings.Contains(res.Message, "portico") {
		t.Fatalf("good secret: %+v", res)
	}
	res = NewChecker(nil).Keycloak(context.Background(), srv.URL, s, "bad-secret")
	if res.OK || !strings.Contains(res.Message, "client secret") {
		t.Fatalf("bad secret: %+v", res)
	}
}

func TestCheckOIDCIssuerMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"issuer":"https://id.example.test/realms/portico","authorization_endpoint":"https://id.example.test/realms/portico/protocol/openid-connect/auth","token_endpoint":"x","jwks_uri":"x"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s := sample()
	res := NewChecker(nil).OIDC(context.Background(), srv.URL, s)
	if !res.OK {
		t.Fatalf("matching issuer: %+v", res)
	}
	s.OIDC.Issuer = "https://other.example.test/realms/portico"
	res = NewChecker(nil).OIDC(context.Background(), srv.URL, s)
	if res.OK || !strings.Contains(res.Message, "issuer") {
		t.Fatalf("mismatch: %+v", res)
	}
}

// A mistyped realm makes the token endpoint answer 404: the check names
// the realm instead of blaming the client secret.
func TestCheckKeycloakRealmTypoNamesTheRealm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/realms/portico-typo/") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","expires_in":60}`))
	}))
	defer srv.Close()
	s := sample()
	s.Keycloak.Realm = "portico-typo"
	res := NewChecker(nil).Keycloak(context.Background(), srv.URL, s, "good-secret")
	if res.OK || !strings.Contains(res.Message, `realm "portico-typo" not found`) {
		t.Fatalf("realm typo: %+v", res)
	}
}
