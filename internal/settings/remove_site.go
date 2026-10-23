package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// PrismObjects names what Portico made in the site's Prism Central, as
// the site records it: its identity provider, its service account and
// the account's key. Removing a site without credentials leaves them
// there (sites usability design 4.1).
func (s Site) PrismObjects() []string {
	out := []string{}
	if s.SAMLIdPName != "" || s.SAMLIdPExtID != "" {
		out = append(out, s.idpLabel())
	}
	if s.ServiceAccountExtID != "" || s.APIKey != "" {
		out = append(out, s.accountLabel(), s.keyLabel())
	}
	return out
}

func (s Site) idpLabel() string {
	switch {
	case s.SAMLIdPExtID == "":
		return "identity provider " + s.SAMLIdPName
	case s.SAMLIdPName == "":
		return "identity provider " + s.SAMLIdPExtID
	}
	return fmt.Sprintf("identity provider %s (%s)", s.SAMLIdPName, s.SAMLIdPExtID)
}

func (s Site) accountLabel() string {
	if s.ServiceAccountExtID == "" {
		return "Portico's service account"
	}
	return "Portico's service account " + s.ServiceAccountExtID
}

func (s Site) keyLabel() string {
	if s.APIKeyExtID == "" {
		return "the service account's API key"
	}
	return "the service account's API key " + s.APIKeyExtID
}

// RemoveFromPrismCentral deletes Portico's objects in a site's Prism
// Central with an administrator's credentials (sites usability design
// 4.1, "Removing a site"): its identity provider, after the users Prism
// Central made at sign-in through it (while one exists the provider
// cannot go, IAM-21016, write-ups 15 and 26), and the key Portico
// holds. The service account stays and is named as left: the design
// revokes the credential, and the account may be one the MSP made. The
// certificate (the setup's first step, with no new confirmation) and the
// credentials are checked before anything is deleted, and either refusal
// is the error; a deletion that fails after that is named as left, with
// its reason. The credentials live in this call's client only.
func (b *Bootstrapper) RemoveFromPrismCentral(ctx context.Context, doc Settings, siteID string, in SetupInput) (deleted, left []string, err error) {
	site, ok := doc.Site(siteID)
	switch {
	case !ok:
		return nil, nil, &StateError{Msg: fmt.Sprintf("no site with id %q", siteID)}
	case in.Username == "" || in.Password == "":
		return nil, nil, &StateError{Msg: "the Prism Central username and password are required"}
	}
	ctx, cancel := context.WithTimeout(ctx, SetupDeadline)
	defer cancel()
	in.Pin = ""
	pc, _, _, err := adminClient(ctx, site, in, b.log)
	if err != nil {
		return nil, nil, &StateError{Msg: err.Error()}
	}
	if _, err := pc.ListClusters(ctx); err != nil {
		return nil, nil, &StateError{Msg: credentialRefusal(site.URL, err)}
	}
	deleted, left = []string{}, []string{}
	gone := func(label string, err error) {
		if err != nil {
			left = append(left, label+": not deleted, "+refusal(err))
			return
		}
		deleted = append(deleted, label)
	}

	// The identity provider: the stored one, or Portico's by name with
	// this site's issuer; a provider of that name naming another site is
	// not Portico's to delete.
	if site.SAMLIdPName != "" || site.SAMLIdPExtID != "" {
		idpID := site.SAMLIdPExtID
		if idpID == "" {
			idps, err := pc.ListSAMLIdPs(ctx)
			if err != nil {
				gone(site.idpLabel(), err)
			} else if own, _ := ownIdP(idps, doc, site, site.SAMLIdPName); own != nil {
				idpID = own.ExtID
			}
		}
		if idpID != "" {
			b.deleteSAMLUsers(ctx, pc, idpID, gone)
			gone(site.idpLabel(), pc.DeleteSAMLIdP(ctx, idpID))
		}
	}

	// The key, by the ids the setup stored; an older key without them is
	// left, as is the account.
	if site.ServiceAccountExtID != "" || site.APIKey != "" {
		switch {
		case site.ServiceAccountExtID != "" && site.APIKeyExtID != "":
			err := pc.DeleteAPIKey(ctx, site.ServiceAccountExtID, site.APIKeyExtID)
			if nutanix.IsNotFound(err) {
				err = nil
			}
			gone(site.keyLabel(), err)
		default:
			left = append(left, site.keyLabel()+": its id was not recorded")
		}
		left = append(left, site.accountLabel())
	}
	b.log.Info("site removed from its Prism Central", "site", siteID, "deleted", len(deleted), "left", len(left))
	return deleted, left, nil
}

// deleteSAMLUsers deletes the users Prism Central made at sign-in through
// the identity provider; without it they can no longer sign in. They are
// counted, never named: their names are the tenants' users' addresses.
func (b *Bootstrapper) deleteSAMLUsers(ctx context.Context, pc *nutanix.Client, idpID string, gone func(string, error)) {
	users, err := pc.ListSAMLUsers(ctx, idpID)
	if err != nil {
		gone("the identity provider's SAML users", err)
		return
	}
	if len(users) == 0 {
		return
	}
	for _, u := range users {
		if err = pc.DeleteUser(ctx, u.ExtID); err != nil {
			break
		}
	}
	gone(fmt.Sprintf("the identity provider's %d SAML users (made by Prism Central at sign-in)", len(users)), err)
}

// refusal is why a deletion did not happen: Prism Central's own answer
// when it gave one, else the class of the connection error.
func refusal(err error) string {
	var api *nutanix.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("Prism Central answered %d: %s", api.Status, api.Message)
	}
	return errClass(err)
}
