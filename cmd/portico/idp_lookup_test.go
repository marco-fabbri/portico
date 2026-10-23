package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

func TestSignInUsesStoredIdPWithoutListing(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected", 500)
	}))
	defer srv.Close()
	pc, err := nutanix.New(srv.URL, "k", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	site := settings.Site{SAMLIdPName: "portico-keycloak", SAMLIdPExtID: "idp-7"}
	got, err := idpLookup(pc, site)(context.Background())
	if err != nil || got != "idp-7" {
		t.Fatalf("lookup = %q, %v", got, err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d calls to Prism Central for a stored id", n)
	}
}
