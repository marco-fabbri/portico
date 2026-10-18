package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
)

// PrismInput is the Prism Central bootstrap request: an administrator's
// one-time credentials (any account that can create users and manage
// authorization policies, design §4.5), the connection, and the name of
// the service account to create.
type PrismInput struct {
	Username       string
	Password       string
	URL            string
	TLSVerify      bool
	CAPEM          string
	ServiceAccount string // defaults to "portico"
}

// PrismResult is what the bootstrap reports; the key is in the returned
// Settings only. The embedded CheckResult is the verification made with
// the new key, whose lists fill the pickers.
type PrismResult struct {
	CheckResult
	ServiceAccount      string    `json:"service_account,omitempty"`
	ServiceAccountExtID string    `json:"service_account_ext_id,omitempty"`
	KeyName             string    `json:"key_name,omitempty"`
	KeyExpires          time.Time `json:"key_expires,omitempty"`
	Created             []string  `json:"created"`
}

const (
	defaultServiceAccount = "portico"
	projectManagerPolicy  = "Project Manager_acp"
)

var reServiceAccount = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// PrismCentral creates the service account (or finds it), issues it a
// new API key, puts it in the Project Manager policy and verifies the
// key by listing clusters and containers. A failure after the key exists
// revokes it. Refusals and unreachable hosts are results with OK false;
// a precondition on the input is a *StateError.
func (b *Bootstrapper) PrismCentral(ctx context.Context, s Settings, in PrismInput) (PrismResult, Settings, error) {
	name := in.ServiceAccount
	if strings.TrimSpace(name) == "" {
		name = defaultServiceAccount
	}
	if in.Username == "" || in.Password == "" {
		return PrismResult{}, s, &StateError{Msg: "the Prism Central username and password are required"}
	}
	u, err := url.Parse(in.URL)
	if in.URL == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return PrismResult{}, s, &StateError{Msg: fmt.Sprintf("the Prism Central URL %q is not valid", in.URL)}
	}
	if !reServiceAccount.MatchString(name) {
		return PrismResult{}, s, &StateError{Msg: "the service account name must be lower-case letters, digits, dots, dashes and underscores"}
	}
	res := PrismResult{Created: []string{}, ServiceAccount: name}
	tlsCfg, err := proxy.PrismTLSPEM(u.Hostname(), []byte(in.CAPEM), in.TLSVerify)
	if err != nil {
		return PrismResult{}, s, &StateError{Msg: "the CA certificate is not usable: " + err.Error()}
	}
	pc, err := nutanix.NewWithPassword(in.URL, in.Username, in.Password, tlsCfg, b.log)
	if err != nil {
		return PrismResult{}, s, &StateError{Msg: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	remote := func(err error) (PrismResult, Settings, error) {
		var apiErr *nutanix.APIError
		res.OK = false
		switch {
		case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
			res.Message = fmt.Sprintf("Prism Central refused the credentials (%d)", apiErr.Status)
		default:
			res.Message = prismError(in.URL, err)
		}
		return res, s, nil
	}

	// Service account.
	user, err := pc.FindUser(ctx, name)
	switch {
	case nutanix.IsNotFound(err):
		host := s.PrismHost
		if strings.TrimSpace(host) == "" {
			host = "portico.invalid"
		}
		user, err = pc.CreateServiceAccount(ctx, name, "Portico service account", "Portico: tenant provisioning", name+"@"+host)
		if err != nil {
			return remote(err)
		}
		b.log.Info("bootstrap prism central: service account created", "username", name, "ext_id", user.ExtID)
		res.Created = append(res.Created, "service account "+name)
	case err != nil:
		return remote(err)
	case user.UserType != "SERVICE_ACCOUNT":
		res.Message = fmt.Sprintf("a user named %q exists in Prism Central and is not a service account (%s): choose another name", name, user.UserType)
		return res, s, nil
	}
	res.ServiceAccountExtID = user.ExtID

	// API key: a new one every bootstrap, dated so the administrator can
	// tell them apart in Prism Central.
	key, err := pc.CreateAPIKey(ctx, user.ExtID, "portico-"+time.Now().UTC().Format("20060102-150405"), "Portico appliance")
	if err != nil {
		return remote(err)
	}
	b.log.Info("bootstrap prism central: api key created", "username", name, "key", key.Name, "expires", key.ExpiryTime)
	res.Created = append(res.Created, "api key "+key.Name)
	res.KeyName, res.KeyExpires = key.Name, key.ExpiryTime
	revoke := func() {
		if err := pc.DeleteAPIKey(ctx, user.ExtID, key.ExtID); err != nil {
			b.log.Warn("bootstrap prism central: the new key could not be revoked after a failure", "key", key.Name, "err", err.Error())
			return
		}
		b.log.Info("bootstrap prism central: api key revoked after a failure", "key", key.Name)
	}
	failed := func(msg string) (PrismResult, Settings, error) {
		revoke()
		res.OK, res.Message = false, msg
		return res, s, nil
	}

	// Policy membership.
	pol, err := pc.FindAuthorizationPolicy(ctx, projectManagerPolicy)
	if nutanix.IsNotFound(err) {
		return failed(fmt.Sprintf("authorization policy %q not found", projectManagerPolicy))
	}
	if err != nil {
		return failed(prismError(in.URL, err))
	}
	added, err := pc.AddPolicyUser(ctx, pol.ExtID, user.ExtID)
	if err != nil {
		return failed("the policy could not be updated: " + prismError(in.URL, err))
	}
	if added {
		b.log.Info("bootstrap prism central: service account added to the policy", "username", name, "policy", projectManagerPolicy)
		res.Created = append(res.Created, "policy member")
	}

	// Verification with the new key, which also fills the pickers.
	out := s
	out.PrismCentral.URL, out.PrismCentral.TLSVerify, out.PrismCentral.CAPEM = in.URL, in.TLSVerify, in.CAPEM
	check := NewChecker(b.log).PrismCentral(ctx, out, key.Key)
	if !check.OK {
		return failed("the new key was refused: " + check.Message)
	}
	out.PrismCentral.APIKey = Secret(key.Key)
	res.CheckResult = check
	res.Message = fmt.Sprintf("Prism Central bootstrap done: created %s; %s", strings.Join(res.Created, ", "), check.Message)
	return res, out, nil
}
