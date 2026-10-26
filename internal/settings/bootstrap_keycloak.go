package settings

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
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
		case strings.HasPrefix(err.Error(), "keycloak: token:"), errors.As(err, new(*url.Error)):
			// No answer at all (a dropped connection while it restarts
			// with its public name): the caller waits and tries again.
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
	oidcUUID, created, err := kc.EnsureClient(ctx, oidcRep(s.PortalHost))
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

// oidcRep is Portico's OIDC client for the portal's public name.
func oidcRep(portalHost string) map[string]any {
	return map[string]any{"clientId": oidcClientID, "protocol": "openid-connect", "enabled": true, "name": "Portico", "publicClient": false,
		"standardFlowEnabled": true, "directAccessGrantsEnabled": false, "implicitFlowEnabled": false, "serviceAccountsEnabled": false,
		"frontchannelLogout": false, "redirectUris": []string{"https://" + portalHost + "/.portico/callback"}, "webOrigins": []string{},
		"attributes": map[string]string{"pkce.code.challenge.method": "S256", "post.logout.redirect.uris": "https://" + portalHost + "/"}}
}

// The realm Portico creates by itself.
const (
	AutoRealm            = "portico"
	AutoRealmDisplayName = "Portico MSP"
)

// KeycloakAuto is the Identity card without credentials typed by anyone
// (design §4.5). On a new appliance the installer leaves a copy of
// Keycloak's temporary administrator in bootstrapFile, readable by
// portico alone: KeycloakAuto creates the realm and Portico's clients
// with it, stores the settings, then deletes that administrator from the
// master realm and the file, in this order, so a restart in between
// finishes the job at the next run. Without the file and with the realm
// set (the public names changed later), Portico's own admin client
// updates the OIDC client and the issuer follows the identity name.
func (b *Bootstrapper) KeycloakAuto(ctx context.Context, keycloakURL string, s Settings, bootstrapFile string) (KeycloakResult, Settings, error) {
	if strings.TrimSpace(s.PortalHost) == "" || strings.TrimSpace(s.IDHost) == "" {
		return KeycloakResult{}, s, &StateError{Msg: "save the public names first: the clients need them"}
	}
	user, pw, err := readBootstrapFile(bootstrapFile)
	if err != nil {
		return KeycloakResult{}, s, err
	}
	realmDone := s.Keycloak.Realm != "" && s.Keycloak.AdminClientID != "" && s.Keycloak.AdminClientSecret != "" && s.OIDC.ClientSecret != ""
	if user == "" && !realmDone {
		return KeycloakResult{}, s, &StateError{Msg: "Keycloak's temporary administrator is gone (it is used once and deleted): use an existing realm"}
	}
	res := KeycloakResult{Created: []string{}}
	if !realmDone {
		res, s, err = b.Keycloak(ctx, keycloakURL, s, KeycloakInput{AdminUsername: user, AdminPassword: pw, Realm: AutoRealm, DisplayName: AutoRealmDisplayName})
		if err != nil || !res.OK || (b.svc != nil && !res.Stored) {
			return res, s, err
		}
	}
	if user != "" {
		if msg := b.deleteTemporaryAdmin(ctx, keycloakURL, user, pw); msg != "" {
			res.OK, res.Message = false, msg
			return res, s, nil
		}
		if err := os.Remove(bootstrapFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			res.OK, res.Message = false, "the temporary Keycloak administrator is deleted but its copy is not: "+err.Error()
			return res, s, nil
		}
		b.log.Info("bootstrap keycloak: temporary administrator deleted", "user", user)
		if !realmDone {
			res.Message += "; the temporary Keycloak administrator is deleted"
			return res, s, nil
		}
	}
	return b.followNames(ctx, keycloakURL, s)
}

// deleteTemporaryAdmin deletes the first boot's administrator from the
// master realm; the message is empty on success. Credentials Keycloak
// refuses mean it is gone already (a restart after the delete).
func (b *Bootstrapper) deleteTemporaryAdmin(ctx context.Context, keycloakURL, user, pw string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	kc := keycloak.NewWithPassword(keycloakURL, "master", user, pw, &http.Client{Timeout: 15 * time.Second}, b.log)
	err := kc.DeleteMasterUser(ctx, user)
	var se *keycloak.StatusError
	if err == nil || (errors.As(err, &se) && se.Op == "token" && se.Status == http.StatusUnauthorized) {
		return ""
	}
	return "the realm is ready but the temporary Keycloak administrator was not deleted: " + kcErr(err)
}

// followNames points Portico's OIDC client and the issuer at the current
// public names, with Portico's own admin client. A realm typed by hand
// with clients of other names is left alone.
func (b *Bootstrapper) followNames(ctx context.Context, keycloakURL string, s Settings) (KeycloakResult, Settings, error) {
	res := KeycloakResult{Created: []string{}, Realm: s.Keycloak.Realm, AdminClientID: s.Keycloak.AdminClientID, ClientID: s.OIDC.ClientID}
	if s.OIDC.ClientID != oidcClientID || s.Keycloak.AdminClientID != adminClientID {
		res.OK, res.Issuer, res.Message = true, s.OIDC.Issuer, "Keycloak realm "+s.Keycloak.Realm+" is configured"
		return res, s, nil
	}
	if !waitKeycloakReady(ctx, keycloakURL) {
		res.Message = "Keycloak is not answering yet"
		return res, s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	kc := keycloak.New(keycloakURL, s.Keycloak.Realm, s.Keycloak.AdminClientID, string(s.Keycloak.AdminClientSecret), &http.Client{Timeout: 15 * time.Second}, b.log)
	if _, _, err := kc.EnsureClient(ctx, oidcRep(s.PortalHost)); err != nil {
		res.Message = "Keycloak refused the update of Portico's OIDC client: " + kcErr(err)
		return res, s, nil
	}
	issuer := "https://" + s.IDHost + "/realms/" + s.Keycloak.Realm
	if s.OIDC.Issuer != issuer {
		if _, err := b.store(ctx, func(d *Settings) { d.OIDC.Issuer = issuer }); err != nil {
			res.Message = "the issuer was not stored: " + err.Error()
			return res, s, nil
		}
		s.OIDC.Issuer = issuer
	}
	res.OK, res.Issuer, res.Message = true, issuer, "Keycloak realm "+s.Keycloak.Realm+" is ready for "+s.PortalHost
	return res, s, nil
}

// kcErr says what Keycloak answered, or why it did not.
func kcErr(err error) string {
	var se *keycloak.StatusError
	if errors.As(err, &se) {
		return strings.TrimPrefix(err.Error(), "keycloak: ")
	}
	return errClass(err)
}

// readBootstrapFile reads the installer's copy of the temporary
// administrator; an absent file is no administrator, not an error.
func readBootstrapFile(path string) (user, pw string, err error) {
	if path == "" {
		return "", "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("keycloak bootstrap file: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "KC_BOOTSTRAP_ADMIN_USERNAME="); ok {
			user = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "KC_BOOTSTRAP_ADMIN_PASSWORD="); ok {
			pw = strings.TrimSpace(v)
		}
	}
	if user == "" || pw == "" {
		return "", "", fmt.Errorf("keycloak bootstrap file %s: no username or password", path)
	}
	return user, pw, nil
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
