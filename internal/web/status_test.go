package web

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// The admin UI answers a service error with the status the API gives it:
// the two used to keep their own tables and drifted (final review of
// Milestone 6: a tenant connected to an unfinished site got 500 in the UI,
// 409 in the API).
func TestUIStatusesMatchTheAPI(t *testing.T) {
	if got := statusOf(fmt.Errorf("connect: %w", tenant.ErrSiteIncomplete)); got != http.StatusConflict {
		t.Errorf("ErrSiteIncomplete: UI %d, want 409", got)
	}
	if got := siteStatus(&api.PinChangedError{Fingerprint: "AB:CD"}); got != http.StatusConflict {
		t.Errorf("PinChangedError: UI %d, want 409", got)
	}
}
