package tenant

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/store"
)

// UserGroupsLister is the part of the Keycloak admin client the resolver
// uses.
type UserGroupsLister interface {
	UserGroups(ctx context.Context, userID string) ([]keycloak.Group, error)
}

// Resolver returns the function the login uses to name a user's tenant
// (design sites §4.3): the tenant whose group <groupPrefix><slug> the user
// is in and whose row exists. No such group gives ""; a user in two
// tenants gives "" too, logged, because a person belongs to one tenant.
func Resolver(kc UserGroupsLister, st store.Store, groupPrefix string, log *slog.Logger) func(ctx context.Context, subject string) (string, error) {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, subject string) (string, error) {
		groups, err := kc.UserGroups(ctx, subject)
		if err != nil {
			return "", err
		}
		var found []string
		for _, g := range groups {
			slug, ok := strings.CutPrefix(g.Name, groupPrefix)
			if !ok || !ValidSlug(slug) {
				continue
			}
			if _, err := st.GetTenant(ctx, slug); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return "", err
				}
				continue
			}
			found = append(found, slug)
		}
		switch len(found) {
		case 1:
			return found[0], nil
		case 0:
			return "", nil
		default:
			log.Warn("login: the user is in several tenant groups, none is used", "subject", subject, "tenants", found)
			return "", nil
		}
	}
}
