package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

// fakeIDP is a minimal OpenID Connect provider for tests: discovery,
// authorization (immediate redirect with a code), token (PKCE checked,
// RS256 ID token), JWKS and end-session endpoints.
type fakeIDP struct {
	t              *testing.T
	srv            *httptest.Server
	key            *rsa.PrivateKey
	issuerOverride string
	down           atomic.Bool
	clientID       string
	clientSecret   string
	subject, email string

	mu        sync.Mutex
	codes     map[string]codeRecord
	authCalls int
}

type codeRecord struct {
	challenge, nonce, redirect string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: key, clientID: "portico", clientSecret: "secret", subject: "alice", email: "alice@example.test", codes: map[string]codeRecord{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/auth", f.auth)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/jwks", f.jwks)
	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
					return
				}
			}
			http.Error(w, "down", 503)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base URL the IdP is reached at.
func (f *fakeIDP) URL() string { return f.srv.URL }

func (f *fakeIDP) issuer() string {
	if f.issuerOverride != "" {
		return f.issuerOverride
	}
	return f.srv.URL
}

func (f *fakeIDP) discovery(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.issuer(),
		"authorization_endpoint":                f.srv.URL + "/auth",
		"token_endpoint":                        f.srv.URL + "/token",
		"jwks_uri":                              f.srv.URL + "/jwks",
		"end_session_endpoint":                  f.srv.URL + "/logout",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIDP) auth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.authCalls++
	code := NewSessionID()
	f.codes[code] = codeRecord{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
	f.mu.Unlock()
	if q.Get("client_id") != f.clientID || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
		http.Error(w, "bad authorization request", 400)
		return
	}
	u, _ := url.Parse(q.Get("redirect_uri"))
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != f.clientID || secret != f.clientSecret {
		http.Error(w, `{"error":"invalid_client"}`, 401)
		return
	}
	f.mu.Lock()
	rec, found := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	if r.PostForm.Get("grant_type") != "authorization_code" || !found || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != rec.challenge || r.PostForm.Get("redirect_uri") != rec.redirect {
		http.Error(w, `{"error":"invalid_grant"}`, 400)
		return
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	claims := map[string]any{
		"iss": f.issuer(), "sub": f.subject, "aud": f.clientID, "exp": now.Add(time.Minute).Unix(),
		"iat": now.Unix(), "nonce": rec.nonce, "email": f.email,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		f.t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60, "id_token": raw})
}

func (f *fakeIDP) jwks(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
}
