package settings

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/marco-fabbri/portico/internal/proxy"
)

func TestInspectUntrustedShowsFingerprint(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	info, err := InspectCertificate(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Trusted || info.VerifyError == "" {
		t.Fatalf("trusted = %v (%q)", info.Trusted, info.VerifyError)
	}
	if info.Fingerprint != proxy.Fingerprint(srv.Certificate().Raw) {
		t.Fatalf("fingerprint = %q", info.Fingerprint)
	}
	if len(info.Names) == 0 || info.NotAfter.IsZero() {
		t.Fatalf("names %v, not after %v", info.Names, info.NotAfter)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the inspection sent %d HTTP requests", n)
	}
}

func TestInspectTrustedWithCAPEM(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	info, err := InspectCertificate(context.Background(), srv.URL, ca)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Trusted || info.Issuer == "" {
		t.Fatalf("info = %+v", info)
	}
}

func TestInspectNothingAnswers(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := srv.URL
	srv.Close()
	if _, err := InspectCertificate(context.Background(), u, nil); err == nil {
		t.Fatal("no error for a closed port")
	}
}
