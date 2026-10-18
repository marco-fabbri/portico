package tenant

import (
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/settings"
)

// The temporary password of a first user must pass the policy the
// Keycloak bootstrap sets on the realm (length 12, not the username,
// not the email): the generator and the policy are pinned together.
func TestTemporaryPasswordSatisfiesTheRealmPolicy(t *testing.T) {
	if settings.RealmPasswordPolicy != "length(12) and notUsername and notEmail" {
		t.Fatalf("policy changed to %q: re-check the generator", settings.RealmPasswordPolicy)
	}
	for i := 0; i < 50; i++ {
		p, err := temporaryPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) < 12 || strings.ContainsAny(p, "@ ") || strings.Contains(strings.ToLower(p), "carol") {
			t.Fatalf("password %q would fail the policy", p)
		}
	}
}
