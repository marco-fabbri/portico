package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/exposure"
)

// withExposure points the exposure card at a temporary request
// directory and an installed self-signed certificate.
func withExposure(t *testing.T) (dir, certFile string, opt envOpt) {
	t.Helper()
	dir, tls := t.TempDir(), t.TempDir()
	c, _, err := exposure.SelfSigned([]string{"portico"})
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(tls, "public.crt")
	os.WriteFile(certFile, c, 0o644)
	return dir, certFile, func(o *Options) { o.Exposure = ExposureOptions{Dir: dir, CertFile: certFile} }
}

// upload posts the exposure form as multipart, with the session cookie.
func (e *env) upload(t *testing.T, fields map[string]string, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("_csrf", e.csrf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for k, v := range files {
		fw, _ := mw.CreateFormFile(k, k+".pem")
		fw.Write(v)
	}
	mw.Close()
	r := httptest.NewRequest("POST", "https://127.0.0.1:8443/exposure", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(e.cookie)
	r.RemoteAddr = "10.0.0.9:4000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// exposureEnv is a logged-in admin UI with the public names saved.
func exposureEnv(t *testing.T, names bool) (*env, string, string) {
	dir, certFile, opt := withExposure(t)
	e := newEnv(t, true, opt)
	svc, _, _ := withSetup(t, e, true)
	if !names {
		s, _ := svc.Get(context.Background())
		s.PortalHost, s.IDHost = "", ""
		svc.Set(context.Background(), s)
	}
	e.login(t)
	return e, dir, certFile
}

func pairFor(t *testing.T, names ...string) (c, k []byte) {
	c, k, err := exposure.SelfSigned(names)
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestExposureUploadRefusesAMismatchedPair(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	c, _ := pairFor(t, "prism.example.test", "id.example.test")
	_, k := pairFor(t, "prism.example.test", "id.example.test")
	rec := e.upload(t, map[string]string{"mode": "certificate"}, map[string][]byte{"cert": c, "key": k})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "the key does not match the certificate") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files written: %v", entries)
	}
	// A pair for other names is refused with the name it lacks.
	c, k = pairFor(t, "prism.example.test")
	rec = e.upload(t, map[string]string{"mode": "certificate"}, map[string][]byte{"cert": c, "key": k})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "does not name id.example.test") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestExposureUploadWritesARequest(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	c, k := pairFor(t, "prism.example.test", "id.example.test")
	rec := e.upload(t, map[string]string{"mode": "certificate"}, map[string][]byte{"cert": c, "key": k})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Applying") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, f := range []string{"public.crt", "public.key", "request.json"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, err, fi)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "public.key"))
	if !bytes.Equal(got, k) {
		t.Fatal("key not written as uploaded")
	}
	// request.json last: the path unit fires on it.
	ri, _ := os.Stat(filepath.Join(dir, "request.json"))
	ki, _ := os.Stat(filepath.Join(dir, "public.key"))
	if ri.ModTime().Before(ki.ModTime()) {
		t.Fatal("request.json written before the key")
	}
	var req exposure.Request
	b, _ := os.ReadFile(filepath.Join(dir, "request.json"))
	if json.Unmarshal(b, &req) != nil || req.Mode != "direct" || req.ID == "" || req.SelfSigned {
		t.Fatalf("request %s", b)
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") || strings.Contains(e.log.String(), "PRIVATE KEY") {
		t.Fatal("key echoed")
	}
	// A second change while one is being applied is refused.
	if rec := e.upload(t, map[string]string{"mode": "selfsigned"}, nil); rec.Code != 409 {
		t.Fatalf("second request: %d", rec.Code)
	}
}

func TestExposureSelfSignedCoversTheNames(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	rec := e.upload(t, map[string]string{"mode": "selfsigned"}, nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	c, _ := os.ReadFile(filepath.Join(dir, "public.crt"))
	k, _ := os.ReadFile(filepath.Join(dir, "public.key"))
	if _, err := exposure.Validate(c, k, []string{"prism.example.test", "id.example.test"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestExposureUploadLimits(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	big := bytes.Repeat([]byte("A"), 65<<10)
	rec := e.upload(t, map[string]string{"mode": "certificate"}, map[string][]byte{"cert": big, "key": []byte("x")})
	if rec.Code != 413 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files written: %v", entries)
	}
}

func TestExposureTunnelTokenIsNotEchoed(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	token := base64.StdEncoding.EncodeToString([]byte(`{"a":"acct","t":"tun","s":"c2VjcmV0LXZhbHVl"}`))
	rec := e.upload(t, map[string]string{"mode": "tunnel", "token": token}, nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "token")); string(got) != token+"\n" {
		t.Fatalf("token file %q", got)
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(e.log.String(), token) {
		t.Fatal("token echoed")
	}
	// A pasted command line is refused, and not echoed either.
	os.Remove(filepath.Join(dir, "request.json"))
	bad := "cloudflared service install " + token
	rec = e.upload(t, map[string]string{"mode": "tunnel", "token": bad}, nil)
	if rec.Code != 400 || strings.Contains(rec.Body.String(), token) || !strings.Contains(rec.Body.String(), "paste only the token") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestExposureNeedsPublicNames(t *testing.T) {
	e, dir, _ := exposureEnv(t, false)
	rec := e.upload(t, map[string]string{"mode": "selfsigned"}, nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "public names") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files written: %v", entries)
	}
}

func TestExposureStatusShowsTheResult(t *testing.T) {
	e, dir, _ := exposureEnv(t, true)
	os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"id":"r1","ok":false,"message":"cloudflared stopped right after its start; Portico keeps its current exposure","mode":"tunnel"}`), 0o644)
	rec := e.do("GET", "/exposure/status", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "cloudflared stopped right after its start") || !strings.Contains(body, "Self-signed") || !strings.Contains(body, "portico") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	// While a request waits, the fragment polls; once its result is in, it stops.
	os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"id":"r2","mode":"direct"}`), 0o600)
	if body := e.do("GET", "/exposure/status", nil, true).Body.String(); !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatalf("no polling while pending: %s", body)
	}
	os.Remove(filepath.Join(dir, "request.json"))
	os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"id":"r2","ok":true,"message":"certificate installed","mode":"direct"}`), 0o644)
	if body := e.do("GET", "/exposure/status", nil, true).Body.String(); strings.Contains(body, "every 2s") || !strings.Contains(body, "certificate installed") {
		t.Fatalf("still polling or no result: %s", body)
	}
	// The setup page carries the card.
	if body := e.do("GET", "/setup", nil, true).Body.String(); !strings.Contains(body, "Public names and exposure") || !strings.Contains(body, `action="/exposure"`) {
		t.Fatalf("setup page lacks the exposure card")
	}
}
