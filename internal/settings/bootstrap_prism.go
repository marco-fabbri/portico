package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

const (
	defaultServiceAccount = "portico"
	projectManagerPolicy  = "Project Manager_acp"
)

var reServiceAccount = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// serviceAccountOutcome is what the service account step leaves: the
// account, the key it issued (nil when the stored one was kept), the
// check made with the key, and what it created.
type serviceAccountOutcome struct {
	User    *nutanix.User
	Key     *nutanix.APIKey
	Kept    bool
	Check   CheckResult
	Created []string
	revoke  func()
}

// discard revokes the key the step issued, which nobody will hold.
func (o *serviceAccountOutcome) discard() {
	if o.revoke == nil {
		return
	}
	o.revoke()
	o.revoke, o.Key = nil, nil
	kept := o.Created[:0]
	for _, c := range o.Created {
		if !strings.HasPrefix(c, "api key") {
			kept = append(kept, c)
		}
	}
	o.Created = kept
}

// credentialRefusal is a Prism Central error as an administrator reads it.
func credentialRefusal(target string, err error) string {
	var apiErr *nutanix.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		return fmt.Sprintf("Prism Central refused the credentials (%d)", apiErr.Status)
	}
	return prismError(target, err)
}

// serviceAccount creates the service account name in Prism Central (or
// finds it), issues it a new API key, puts it in the Project Manager
// policy and verifies the key on site's connection, which also checks
// the version. With keep, a stored key that still works for this account
// is kept and no key is issued: running the step again leaves no extra
// credential behind. A failure after the key exists revokes it; the
// message says what failed.
func (b *Bootstrapper) serviceAccount(ctx context.Context, pc *nutanix.Client, s Settings, site Site, name string, keep bool) (serviceAccountOutcome, string) {
	out := serviceAccountOutcome{Created: []string{}}
	user, err := pc.FindUser(ctx, name)
	switch {
	case nutanix.IsNotFound(err):
		host := s.PortalHost
		if strings.TrimSpace(host) == "" {
			host = "portico.invalid"
		}
		user, err = pc.CreateServiceAccount(ctx, name, "Portico service account", "Portico: tenant provisioning", name+"@"+host)
		if err != nil {
			return out, credentialRefusal(site.URL, err)
		}
		b.log.Info("bootstrap prism central: service account created", "username", name, "ext_id", user.ExtID)
		out.Created = append(out.Created, "service account "+name)
	case err != nil:
		return out, credentialRefusal(site.URL, err)
	case user.UserType != "SERVICE_ACCOUNT":
		return out, fmt.Sprintf("a user named %q exists in Prism Central and is not a service account (%s): choose another name", name, user.UserType)
	}
	out.User = user

	if keep && site.APIKey != "" && site.ServiceAccountExtID == user.ExtID {
		if check := NewChecker(b.log).PrismCentral(ctx, site, string(site.APIKey)); check.OK {
			if msg := b.joinPolicy(ctx, pc, site.URL, user, name, &out); msg != "" {
				return out, msg
			}
			out.Kept, out.Check = true, check
			return out, ""
		}
	}

	// API key: a new one every time, dated so the administrator can tell
	// them apart in Prism Central.
	keyName := "portico-" + time.Now().UTC().Format("20060102-150405")
	key, err := pc.CreateAPIKey(ctx, user.ExtID, keyName, "Portico appliance")
	if err != nil {
		// The answer may have been lost after Prism Central created the
		// key: a valid key nobody holds. Look it up by name and revoke it.
		b.revokeByName(pc, user.ExtID, keyName)
		return out, credentialRefusal(site.URL, err)
	}
	b.log.Info("bootstrap prism central: api key created", "username", name, "key", key.Name, "expires", key.ExpiryTime)
	out.Created = append(out.Created, "api key "+key.Name)
	out.Key = key
	out.revoke = func() {
		// The request's context may be what failed (deadline, client gone):
		// the cleanup gets its own bounded one.
		cctx, ccancel := cleanupContext(ctx)
		defer ccancel()
		if err := pc.DeleteAPIKey(cctx, user.ExtID, key.ExtID); err != nil {
			b.log.Warn("bootstrap prism central: the new key could not be revoked after a failure", "key", key.Name, "err", err.Error())
			return
		}
		b.log.Info("bootstrap prism central: api key revoked after a failure", "key", key.Name)
	}
	if msg := b.joinPolicy(ctx, pc, site.URL, user, name, &out); msg != "" {
		out.discard()
		return out, msg
	}
	// Verification with the new key, which also fills the pickers and
	// refuses a Prism Central older than pc.7.6.
	check := NewChecker(b.log).PrismCentral(ctx, site, key.Key)
	if !check.OK {
		out.discard()
		return out, "the new key was refused: " + check.Message
	}
	out.Check = check
	return out, ""
}

// joinPolicy puts the service account in the Project Manager policy.
func (b *Bootstrapper) joinPolicy(ctx context.Context, pc *nutanix.Client, target string, user *nutanix.User, name string, out *serviceAccountOutcome) string {
	pol, err := pc.FindAuthorizationPolicy(ctx, projectManagerPolicy)
	if nutanix.IsNotFound(err) {
		return fmt.Sprintf("authorization policy %q not found", projectManagerPolicy)
	}
	if err != nil {
		return prismError(target, err)
	}
	added, err := pc.AddPolicyUser(ctx, pol.ExtID, user.ExtID)
	if err != nil {
		return "the policy could not be updated: " + prismError(target, err)
	}
	if added {
		b.log.Info("bootstrap prism central: service account added to the policy", "username", name, "policy", projectManagerPolicy)
		out.Created = append(out.Created, "policy member")
	}
	return ""
}

// cleanupContext derives a context for cleanup calls that survives the
// parent's cancellation or deadline, bounded on its own.
func cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
}

// revokeByName revokes the user's key named keyName, if Prism Central
// created it although the creation call failed on Portico's side.
func (b *Bootstrapper) revokeByName(pc *nutanix.Client, userExtID, keyName string) {
	ctx, cancel := cleanupContext(context.Background())
	defer cancel()
	keys, err := pc.ListAPIKeys(ctx, userExtID)
	if err != nil {
		b.log.Warn("bootstrap prism central: could not list the keys after a failed creation", "key", keyName, "err", err.Error())
		return
	}
	for _, k := range keys {
		if k.Name != keyName {
			continue
		}
		if err := pc.DeleteAPIKey(ctx, userExtID, k.ExtID); err != nil {
			b.log.Warn("bootstrap prism central: a key created during a failed call could not be revoked", "key", keyName, "err", err.Error())
			return
		}
		b.log.Info("bootstrap prism central: a key created during a failed call was revoked", "key", keyName)
	}
}
