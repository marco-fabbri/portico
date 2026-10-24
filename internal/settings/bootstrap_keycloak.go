package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
)

// KeycloakInput is the Keycloak bootstrap request: a Keycloak
// administrator's one-time credentials and the realm to create.
type KeycloakInput struct {
	AdminUsername string
	AdminPassword string
	Realm         string
	DisplayName   string
}

// KeycloakResult is what the bootstrap reports; the secrets it read are
// in the returned Settings only.
type KeycloakResult struct {
	OK            bool     `json:"ok"`
	Message       string   `json:"message"`
	Realm         string   `json:"realm,omitempty"`
	AdminClientID string   `json:"admin_client_id,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	Created       []string `json:"created"`
	Stored        bool     `json:"stored"`
}

const (
	adminClientID = "portico-admin"
	oidcClientID  = "portico"
)

// adminClientRoles are the realm-management roles the admin client
// needs: users and groups for tenants, the realm for the login theme,
// clients for the SAML trust.
var adminClientRoles = []string{"manage-users", "view-users", "query-users", "query-groups", "view-realm", "manage-realm", "manage-clients", "view-clients"}

// Keycloak creates the realm, the admin client (service account with
// the realm-management roles) and the OIDC client, and returns the
// settings with the realm, the issuer and both secrets filled in. A
// refusal or an unreachable Keycloak is a result with OK false, not an
// error; a precondition on the settings or the input is a *StateError.
// RealmPasswordPolicy is set on the realm the bootstrap creates: a
// tenant user cannot pick a short password or one made of their own
// name at the first login. The temporary passwords Portico generates
// (20 characters, letters and digits) satisfy it; a realm that already
// exists keeps its own policy (adoption is step 2).
const RealmPasswordPolicy = "length(12) and notUsername and notEmail"

func (b *Bootstrapper) Keycloak(ctx context.Context, keycloakURL string, s Settings, in KeycloakInput) (KeycloakResult, Settings, error) {
	if strings.TrimSpace(s.PortalHost) == "" || strings.TrimSpace(s.IDHost) == "" {
		return KeycloakResult{}, s, &StateError{Msg: "save the public names first: the clients need them"}
	}
	if !reRealm.MatchString(in.Realm) {
		return KeycloakResult{}, s, &StateError{Msg: "the realm name must be lower-case letters, digits and dashes"}
	}
	if in.AdminUsername == "" || in.AdminPassword == "" {
		return KeycloakResult{}, s, &StateError{Msg: "the Keycloak administrator's username and password are required"}
	}
	res := KeycloakResult{Created: []string{}}
	// Saving the public names restarts Keycloak (its hostname file): wait
	// for it to answer before the first call, bounded.
	if !waitKeycloakReady(ctx, keycloakURL) {
		res.Message = "Keycloak is not answering yet: it restarts with its public name after the public names are saved; try again in a minute"
		return res, s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	kc := keycloak.NewWithPassword(keycloakURL, in.Realm, in.AdminUsername, in.AdminPassword, &http.Client{Timeout: 15 * time.Second}, b.log)
	remote := func(err error) (KeycloakResult, Settings, error) {
		res.OK = false
		var se *keycloak.StatusError
		switch {
		case errors.As(err, &se) && se.Op == "token" && se.Status == http.StatusUnauthorized:
			res.Message = "Keycloak refused the administrator credentials"
		case errors.As(err, &se) && se.Op == "token":
			res.Message = fmt.Sprintf("Keycloak answered status %d at the token endpoint", se.Status)
		case errors.As(err, &se):
			res.Message = "Keycloak answered an error: " + strings.TrimPrefix(err.Error(), "keycloak: ")
		case strings.HasPrefix(err.Error(), "keycloak: token:"):
			res.Message = "Keycloak did not answer: " + errClass(err)
		default:
			res.Message = "Keycloak answered an error: " + strings.TrimPrefix(err.Error(), "keycloak: ")
		}
		return res, s, nil
	}

	// Realm.
	display := in.DisplayName
	if strings.TrimSpace(display) == "" {
		display = in.Realm
	}
	_, err := kc.Realm(ctx)
	switch {
	case errors.Is(err, keycloak.ErrNotFound):
		rep := map[string]any{"realm": in.Realm, "enabled": true, "displayName": display, "loginWithEmailAllowed": true, "registrationEmailAsUsername": true, "passwordPolicy": RealmPasswordPolicy}
		if err := kc.CreateRealm(ctx, rep); err != nil {
			return remote(err)
		}
		b.log.Info("bootstrap keycloak: realm created", "realm", in.Realm)
		res.Created = append(res.Created, "realm")
	case err != nil:
		return remote(err)
	}

	// Admin client: a service account with the realm-management roles.
	adminRep := map[string]any{"clientId": adminClientID, "protocol": "openid-connect", "enabled": true, "name": "Portico admin", "publicClient": false,
		"serviceAccountsEnabled": true, "standardFlowEnabled": false, "directAccessGrantsEnabled": false, "implicitFlowEnabled": false,
		"frontchannelLogout": false, "redirectUris": []string{}, "webOrigins": []string{}}
	adminUUID, created, err := kc.EnsureClient(ctx, adminRep)
	if err != nil {
		return remote(err)
	}
	if created {
		b.log.Info("bootstrap keycloak: client created", "client", adminClientID)
		res.Created = append(res.Created, "client "+adminClientID)
	}
	saUser, err := kc.ServiceAccountUserID(ctx, adminUUID)
	if err != nil {
		return remote(err)
	}
	if added, err := kc.EnsureClientRoles(ctx, saUser, "realm-management", adminClientRoles); err != nil {
		return remote(err)
	} else if len(added) > 0 {
		b.log.Info("bootstrap keycloak: roles mapped", "client", adminClientID, "roles", added)
	}
	adminSecret, err := kc.ClientSecret(ctx, adminUUID)
	if err != nil {
		return remote(err)
	}

	// OIDC client for Portico's own login on the public name.
	oidcRep := map[string]any{"clientId": oidcClientID, "protocol": "openid-connect", "enabled": true, "name": "Portico", "publicClient": false,
		"standardFlowEnabled": true, "directAccessGrantsEnabled": false, "implicitFlowEnabled": false, "serviceAccountsEnabled": false,
		"frontchannelLogout": false, "redirectUris": []string{"https://" + s.PortalHost + "/.portico/callback"}, "webOrigins": []string{},
		"attributes": map[string]string{"pkce.code.challenge.method": "S256", "post.logout.redirect.uris": "https://" + s.PortalHost + "/"}}
	oidcUUID, created, err := kc.EnsureClient(ctx, oidcRep)
	if err != nil {
		return remote(err)
	}
	if created {
		b.log.Info("bootstrap keycloak: client created", "client", oidcClientID)
		res.Created = append(res.Created, "client "+oidcClientID)
	}
	oidcSecret, err := kc.ClientSecret(ctx, oidcUUID)
	if err != nil {
		return remote(err)
	}

	patch := func(d *Settings) {
		d.Keycloak.Realm, d.Keycloak.AdminClientID, d.Keycloak.AdminClientSecret = in.Realm, adminClientID, Secret(adminSecret)
		d.OIDC.Issuer, d.OIDC.ClientID, d.OIDC.ClientSecret = "https://"+s.IDHost+"/realms/"+in.Realm, oidcClientID, Secret(oidcSecret)
	}
	out := s
	patch(&out)
	res.Realm, res.AdminClientID, res.ClientID, res.Issuer = in.Realm, adminClientID, oidcClientID, out.OIDC.Issuer
	if len(res.Created) == 0 {
		res.Message = "Keycloak realm " + in.Realm + " and its clients are in place; the secrets were read again"
	} else {
		res.Message = "Keycloak bootstrap done: created " + strings.Join(res.Created, ", ")
	}
	stored, err := b.store(ctx, patch)
	if err != nil {
		// Nothing to undo: the realm and clients stay and a retry reads
		// the secrets again.
		res.Message = "Keycloak bootstrap done but the settings were not stored: " + err.Error()
		return res, s, nil
	}
	res.OK, res.Stored = true, stored
	return res, out, nil
}

// KeycloakReadyWait bounds the wait for Keycloak before a bootstrap;
// KeycloakReadyPoll is the interval. Tests shorten both.
var (
	KeycloakReadyWait = 120 * time.Second
	KeycloakReadyPoll = 2 * time.Second
)

// waitKeycloakReady polls the master realm's public endpoint until
// Keycloak answers at all with something other than a 5xx (a restarting
// container refuses the connection or answers 503), or the wait is over.
func waitKeycloakReady(ctx context.Context, keycloakURL string) bool {
	deadline := time.Now().Add(KeycloakReadyWait)
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(keycloakURL, "/")+"/realms/master", nil)
		if err != nil {
			return false
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(KeycloakReadyPoll):
		}
	}
}
