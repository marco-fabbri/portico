package settings

import (
	"context"
	"errors"
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
func (b *Bootstrapper) Keycloak(ctx context.Context, keycloakURL string, s Settings, in KeycloakInput) (KeycloakResult, Settings, error) {
	if strings.TrimSpace(s.PrismHost) == "" || strings.TrimSpace(s.IDHost) == "" {
		return KeycloakResult{}, s, &StateError{Msg: "save the public names first: the clients need them"}
	}
	if !reRealm.MatchString(in.Realm) {
		return KeycloakResult{}, s, &StateError{Msg: "the realm name must be lower-case letters, digits and dashes"}
	}
	if in.AdminUsername == "" || in.AdminPassword == "" {
		return KeycloakResult{}, s, &StateError{Msg: "the Keycloak administrator's username and password are required"}
	}
	res := KeycloakResult{Created: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	kc := keycloak.NewWithPassword(keycloakURL, in.Realm, in.AdminUsername, in.AdminPassword, &http.Client{Timeout: 15 * time.Second}, b.log)
	remote := func(err error) (KeycloakResult, Settings, error) {
		res.OK = false
		switch {
		case strings.Contains(err.Error(), "token: status"):
			res.Message = "Keycloak refused the administrator credentials"
		case strings.Contains(err.Error(), "token:"):
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
		rep := map[string]any{"realm": in.Realm, "enabled": true, "displayName": display, "loginWithEmailAllowed": true, "registrationEmailAsUsername": true}
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
		"frontchannelLogout": false, "redirectUris": []string{"https://" + s.PrismHost + "/.portico/callback"}, "webOrigins": []string{},
		"attributes": map[string]string{"pkce.code.challenge.method": "S256", "post.logout.redirect.uris": "https://" + s.PrismHost + "/"}}
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

	out := s
	out.Keycloak.Realm, out.Keycloak.AdminClientID, out.Keycloak.AdminClientSecret = in.Realm, adminClientID, Secret(adminSecret)
	out.OIDC.Issuer, out.OIDC.ClientID, out.OIDC.ClientSecret = "https://"+s.IDHost+"/realms/"+in.Realm, oidcClientID, Secret(oidcSecret)
	res.OK, res.Realm, res.AdminClientID, res.ClientID, res.Issuer = true, in.Realm, adminClientID, oidcClientID, out.OIDC.Issuer
	if len(res.Created) == 0 {
		res.Message = "Keycloak realm " + in.Realm + " and its clients are in place; the secrets were read again"
	} else {
		res.Message = "Keycloak bootstrap done: created " + strings.Join(res.Created, ", ")
	}
	return res, out, nil
}
