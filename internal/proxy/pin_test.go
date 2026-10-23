package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

func pinClient(t *testing.T, host string, trust SiteTrust) *http.Client {
	t.Helper()
	cfg, err := PrismTLSFor(host, trust)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
}

func TestPinnedAcceptsMatchingLeaf(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := pinClient(t, "127.0.0.1", SiteTrust{Pin: Fingerprint(srv.Certificate().Raw)})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestPinnedRefusesOtherLeaf(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	// A pin of another certificate: the server's own with its first byte changed.
	got := Fingerprint(srv.Certificate().Raw)
	otherPin := "00" + got[2:]
	if got[:2] == "00" {
		otherPin = "01" + got[2:]
	}
	c := pinClient(t, "127.0.0.1", SiteTrust{Pin: otherPin})
	_, err := c.Get(srv.URL)
	var pm *PinMismatchError
	if !errors.As(err, &pm) {
		t.Fatalf("err = %v, want a PinMismatchError", err)
	}
	if pm.Got != Fingerprint(srv.Certificate().Raw) || pm.Want != otherPin {
		t.Fatalf("mismatch = %+v", pm)
	}
}

func TestPinnedIgnoresNames(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := pinClient(t, "prism.example", SiteTrust{Pin: Fingerprint(srv.Certificate().Raw), Verify: true})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestVerifiedRefusesUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := pinClient(t, u.Hostname(), SiteTrust{Verify: true})
	_, err := c.Get(srv.URL)
	var pm *PinMismatchError
	if err == nil || errors.As(err, &pm) {
		t.Fatalf("err = %v, want an x509 error", err)
	}
}

func TestFingerprintFormat(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	defer srv.Close()
	fp := Fingerprint(srv.Certificate().Raw)
	if len(fp) != 95 || !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(fp) {
		t.Fatalf("fingerprint %q", fp)
	}
}
