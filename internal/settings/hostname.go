package settings

import (
	"fmt"
	"os"
)

// WriteKeycloakHostname writes the environment Keycloak's quadlet loads
// (deploy/quadlets/keycloak.container): KC_HOSTNAME for the identity host
// and the dynamic backchannel, so the SAML descriptor and the OIDC issuer
// carry the public name while Portico talks to Keycloak on the loopback.
// Written atomically; the service's UMask=0077 leaves it 0600, which is
// enough, as systemd reads it as root (nothing in it is secret).
// keycloak-hostname.path restarts Keycloak when it changes.
// An empty host writes nothing: the hosts card has not been saved yet.
func WriteKeycloakHostname(path, idHost string) error {
	if idHost == "" {
		return nil
	}
	if !ValidHost(idHost) {
		return fmt.Errorf("keycloak hostname file: %q is not a host name", idHost)
	}
	content := "KC_HOSTNAME=https://" + idHost + "\nKC_HOSTNAME_BACKCHANNEL_DYNAMIC=true\n"
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return nil // unchanged: no restart of Keycloak
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("keycloak hostname file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("keycloak hostname file: %w", err)
	}
	return nil
}
