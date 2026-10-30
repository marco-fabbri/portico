package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPassthroughForwardsBasicAuthUnchanged(t *testing.T) {
	up, got := recordingUpstream(t, false, nil)
	u, _ := url.Parse(up.URL)
	prism := NewUpstream(UpstreamOptions{Target: u, HostHeader: "10.0.210.20:9440", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h := NewPassthrough(prism, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := httptest.NewRequest("GET", "https://10.0.210.20:9440/PrismGateway/services/rest/v1/cluster", nil)
	r.Header.Set("Authorization", "Basic YWRtaW46cHc=")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || len(*got) != 1 {
		t.Fatalf("got %d upstream %d", rec.Code, len(*got))
	}
	if (*got)[0].Header.Get("Authorization") != "Basic YWRtaW46cHc=" || (*got)[0].Host != "10.0.210.20:9440" {
		t.Errorf("upstream saw %+v", (*got)[0])
	}
}

func TestAdminHealthz(t *testing.T) {
	h := NewAdmin(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://127.0.0.1:8443/healthz", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Errorf("healthz: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://127.0.0.1:8443/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/x: %d", rec.Code)
	}
}

func TestAdminMountsAPIUnderV1(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, r.URL.Path)
	})
	h := NewAdmin(slog.New(slog.NewTextHandler(io.Discard, nil)), api, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://127.0.0.1:8443/api/v1/tenants", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "/api/v1/tenants" {
		t.Fatalf("/api/v1/tenants: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://127.0.0.1:8443/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz with api mounted: %d", rec.Code)
	}
}

// Review Focus 5 of Milestone 2a: /healthz stays open even with a Basic header.
func TestAdminHealthzIgnoresBasicHeader(t *testing.T) {
	h := NewAdmin(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443/healthz", nil)
	r.Header.Set("Authorization", "Basic eDp5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("healthz with Basic: %d", rec.Code)
	}
}

func TestAdminMountsUIAtRoot(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "ui:"+r.URL.Path)
	})
	h := NewAdmin(slog.New(slog.NewTextHandler(io.Discard, nil)), api, ui)
	for path, want := range map[string]int{"/": 202, "/login": 202, "/tenants/x": 202, "/api/v1/tenants": 418, "/healthz": 200} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "https://127.0.0.1:8443"+path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
}
