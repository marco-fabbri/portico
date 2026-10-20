// Package tenant creates, deletes and checks tenants (design §4.4): a
// Keycloak group tied, on every site where the tenant has resources, to a
// Projects 2.0 project through a SAML group and a project-scoped role
// membership (design sites §4.2). Every step is idempotent (find, then
// create) and recorded in the store, so a failed run can be retried and
// the record shows where it stopped. Long operations run in the
// background; one operation per tenant at a time.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

var (
	// ErrBusy is returned while another operation runs on the tenant.
	ErrBusy = errors.New("tenant: operation in progress")
	// ErrState is returned when the tenant's status does not allow the operation.
	ErrState = errors.New("tenant: operation not allowed in this status")
	// ErrExists is returned when a user already exists.
	ErrExists = errors.New("tenant: already exists")
	// ErrUnknownSite: the request names a site that is not configured.
	ErrUnknownSite = errors.New("tenant: no such site")
	// ErrNoSites: no site named while several are configured.
	ErrNoSites = errors.New("tenant: name the sites to place the tenant on")
	// ErrLastSite: a tenant keeps at least one site; delete the tenant instead.
	ErrLastSite = errors.New("tenant: this is the tenant's last site; delete the tenant instead")
	// ErrPlacementExists: the tenant is already on that site.
	ErrPlacementExists = errors.New("tenant: already placed on this site")
	// ErrNoPlacementTarget: neither the request nor the site names a cluster and a storage container.
	ErrNoPlacementTarget = errors.New("tenant: a cluster and a storage container are needed on this site (the site has no defaults)")
)

// Site is one Prism Central the service places tenants on (design sites
// §4.1), with its client, its SAML identity provider and the default
// placement of new tenants.
type Site struct {
	ID, Code                            string
	PC                                  PrismCentral
	SAMLIdPName                         string
	ClusterExtID, StorageContainerExtID string
}

// PrismCentral is the part of the Prism Central client the service uses.
type PrismCentral interface {
	FindProject(ctx context.Context, name string) (*nutanix.Project, error)
	CreateProject(ctx context.Context, id, name, description string) (*nutanix.Project, error)
	DeleteProject(ctx context.Context, extID string) error
	ListResourceGroups(ctx context.Context, projectExtID string) ([]nutanix.ResourceGroup, error)
	CreateResourceGroup(ctx context.Context, name, projectExtID, clusterExtID, containerExtID string) (*nutanix.ResourceGroup, error)
	DeleteResourceGroup(ctx context.Context, extID string) error
	ListSAMLIdPs(ctx context.Context) ([]nutanix.SAMLIdP, error)
	GetSAMLIdP(ctx context.Context, extID string) (*nutanix.SAMLIdP, error)
	ShareIdP(ctx context.Context, idpExtID, projectExtID string) error
	UnshareIdP(ctx context.Context, idpExtID, projectExtID string) error
	FindUserGroup(ctx context.Context, name string) (*nutanix.UserGroup, error)
	CreateUserGroup(ctx context.Context, name, idpExtID string) (*nutanix.UserGroup, error)
	DeleteUserGroup(ctx context.Context, extID string) error
	FindRole(ctx context.Context, displayName string) (*nutanix.Role, error)
	ListRoleMemberships(ctx context.Context) ([]nutanix.RoleMembership, error)
	CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*nutanix.RoleMembership, error)
	DeleteRoleMembership(ctx context.Context, extID string) error
	PoliciesMentioning(ctx context.Context, extID string) ([]string, error)
}

// Keycloak is the part of the Keycloak admin client the service uses.
type Keycloak interface {
	FindGroup(ctx context.Context, name string) (*keycloak.Group, error)
	CreateGroup(ctx context.Context, name string) (*keycloak.Group, error)
	DeleteGroup(ctx context.Context, id string) error
	FindUserByEmail(ctx context.Context, email string) (*keycloak.User, error)
	CreateUser(ctx context.Context, u keycloak.User, temporaryPassword string) (*keycloak.User, error)
	AddUserToGroup(ctx context.Context, userID, groupID string) error
	GroupMembers(ctx context.Context, groupID string) ([]keycloak.User, error)
	SetUserEnabled(ctx context.Context, userID string, enabled bool) error
}

// Config holds the naming settings of the service; placement lives on
// the sites.
type Config struct {
	ProjectPrefix    string        // project name and id = ProjectPrefix + "-" + slug, on every site
	GroupPrefix      string        // Keycloak and SAML group = GroupPrefix + slug
	RoleName         string        // role bound inside the project, "Project Admin" by default
	OperationTimeout time.Duration // context of a background run
}

func (c Config) withDefaults() Config {
	if c.ProjectPrefix == "" {
		c.ProjectPrefix = "portico"
	}
	if c.GroupPrefix == "" {
		c.GroupPrefix = "tenant-"
	}
	if c.RoleName == "" {
		c.RoleName = "Project Admin"
	}
	if c.OperationTimeout <= 0 {
		c.OperationTimeout = 10 * time.Minute
	}
	return c
}

// Service runs tenant operations.
type Service struct {
	st    store.Store
	sites map[string]Site
	order []string
	kc    Keycloak
	cfg   Config
	log   *slog.Logger

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
	hooks   []func(tenantID string)
}

// NewService returns a service over the store, the sites (in the order
// they are shown) and Keycloak.
func NewService(st store.Store, sites []Site, kc Keycloak, cfg Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{st: st, sites: map[string]Site{}, kc: kc, cfg: cfg.withDefaults(), log: log, running: map[string]bool{}}
	for _, site := range sites {
		s.sites[site.ID] = site
		s.order = append(s.order, site.ID)
	}
	return s
}

// Sites returns the configured sites in order.
func (s *Service) Sites() []Site {
	out := make([]Site, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.sites[id])
	}
	return out
}

// SiteCode returns the code of a configured site, or the id itself.
func (s *Service) SiteCode(id string) string {
	if site, ok := s.sites[id]; ok && site.Code != "" {
		return site.Code
	}
	return id
}

// OnPlacementChange registers fn, called with the tenant id whenever its
// placements change (created, active, failed, removed): the routing index
// drops its cached copy (design sites §4.3).
func (s *Service) OnPlacementChange(fn func(tenantID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

func (s *Service) changed(tenantID string) {
	s.mu.Lock()
	hooks := append([]func(string){}, s.hooks...)
	s.mu.Unlock()
	for _, fn := range hooks {
		fn(tenantID)
	}
}

// PlacementRequest places a tenant on a site; an empty cluster or
// container takes the site's default.
type PlacementRequest struct {
	Site, ClusterExtID, StorageContainerExtID string
}

// CreateRequest is the input of Create. Without sites the tenant goes on
// the only configured site; with several configured, Sites is required.
type CreateRequest struct {
	Slug, DisplayName, Contacts string
	Sites                       []PlacementRequest
}

// placementFor resolves a request against the configured sites.
func (s *Service) placementFor(tenantID string, r PlacementRequest) (*store.Placement, error) {
	site, ok := s.sites[r.Site]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSite, r.Site)
	}
	p := &store.Placement{TenantID: tenantID, SiteID: site.ID, Status: store.PlacementCreating,
		ClusterExtID: r.ClusterExtID, StorageContainerExtID: r.StorageContainerExtID, ProjectID: s.projectName(tenantID)}
	if p.ClusterExtID == "" {
		p.ClusterExtID = site.ClusterExtID
	}
	if p.StorageContainerExtID == "" {
		p.StorageContainerExtID = site.StorageContainerExtID
	}
	if p.ClusterExtID == "" || p.StorageContainerExtID == "" {
		return nil, fmt.Errorf("%w: site %s", ErrNoPlacementTarget, site.Code)
	}
	return p, nil
}

func (s *Service) projectName(slug string) string { return s.cfg.ProjectPrefix + "-" + slug }
func (s *Service) groupName(slug string) string   { return s.cfg.GroupPrefix + slug }

// acquire marks the tenant as busy; false when it already is.
func (s *Service) acquire(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// background runs fn for the tenant with the operation timeout, holding
// the per-tenant lock already acquired by the caller.
func (s *Service) background(id string, op string, fn func(ctx context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.release(id)
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.OperationTimeout)
		defer cancel()
		if err := fn(ctx); err != nil {
			s.log.Warn("tenant operation failed", "tenant", id, "op", op, "err", err.Error())
			return
		}
		s.log.Info("tenant operation done", "tenant", id, "op", op)
	}()
}

// Wait blocks until every background operation has finished.
func (s *Service) Wait() { s.wg.Wait() }

// Running reports how many tenant operations are in progress.
func (s *Service) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// Create validates the request, records the tenant and its placements
// as creating and starts provisioning in the background. store.ErrExists
// passes through.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*store.Tenant, error) {
	if !ValidSlug(req.Slug) {
		return nil, ErrInvalidSlug
	}
	reqs := req.Sites
	if len(reqs) == 0 {
		if len(s.order) != 1 {
			return nil, ErrNoSites
		}
		reqs = []PlacementRequest{{Site: s.order[0]}}
	}
	var ps []*store.Placement
	seen := map[string]bool{}
	for _, r := range reqs {
		if seen[r.Site] {
			return nil, fmt.Errorf("%w: %s named twice", ErrPlacementExists, r.Site)
		}
		seen[r.Site] = true
		p, err := s.placementFor(req.Slug, r)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	t := &store.Tenant{ID: req.Slug, DisplayName: req.DisplayName, Contacts: req.Contacts, Status: store.StatusCreating, LastOp: store.OpProvision}
	// The lock comes first: a deletion releases it only after the row is
	// gone, so a Create in that window must not insert an orphan row.
	if !s.acquire(t.ID) {
		return nil, ErrBusy
	}
	if err := s.st.CreateTenant(ctx, t); err != nil {
		s.release(t.ID)
		return nil, err
	}
	for _, p := range ps {
		if err := s.st.CreatePlacement(ctx, p); err != nil {
			s.st.DeleteTenant(ctx, t.ID) // the placements go with the row
			s.release(t.ID)
			return nil, err
		}
	}
	s.changed(t.ID)
	s.background(t.ID, store.OpProvision, func(ctx context.Context) error { return s.provision(ctx, t.ID, false) })
	out := *t
	return &out, nil
}

// AddSite places an active tenant on one more site and provisions it in
// the background; the tenant stays active whatever the new site's
// outcome, which its placement records (design sites §4.2).
func (s *Service) AddSite(ctx context.Context, id string, r PlacementRequest) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	if t.Status != store.StatusActive {
		return fmt.Errorf("%w: %s", ErrState, t.Status)
	}
	p, err := s.placementFor(id, r)
	if err != nil {
		return err
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	if err := s.st.CreatePlacement(ctx, p); err != nil {
		s.release(id)
		if errors.Is(err, store.ErrExists) {
			return fmt.Errorf("%w: %s", ErrPlacementExists, s.SiteCode(p.SiteID))
		}
		return err
	}
	s.changed(id)
	s.background(id, store.OpProvision, func(ctx context.Context) error { return s.provision(ctx, id, true) })
	return nil
}

// RemoveSite takes a tenant off one site in the background: the 2a
// deletion steps on that site, the project too when deleteProject is
// set, then the placement row. The last site cannot be removed.
func (s *Service) RemoveSite(ctx context.Context, id, siteID string, deleteProject bool) error {
	if _, err := s.st.GetTenant(ctx, id); err != nil {
		return err
	}
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		return err
	}
	if len(ps) <= 1 {
		return ErrLastSite
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	p.Status, p.LastError = store.PlacementRemoving, ""
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		s.release(id)
		return err
	}
	s.changed(id)
	s.background(id, "remove-site", func(ctx context.Context) error {
		defer s.changed(id)
		return s.removePlacement(ctx, id, siteID, deleteProject)
	})
	return nil
}

// Provision runs the provisioning steps synchronously. It is what Create
// and Retry run in the background.
func (s *Service) Provision(ctx context.Context, id string) error {
	if !s.acquire(id) {
		return ErrBusy
	}
	defer s.release(id)
	return s.provision(ctx, id, false)
}

// Retry restarts provisioning in the background on every placement that
// is not active: a failed tenant (not after a failed deletion), or an
// active tenant whose added site failed.
func (s *Service) Retry(ctx context.Context, id string) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	keepStatus := false
	switch {
	case t.Status == store.StatusFailed && t.LastOp == store.OpDelete:
		return fmt.Errorf("%w: the last operation was a deletion that failed; delete the tenant again", ErrState)
	case t.Status == store.StatusFailed:
	case t.Status == store.StatusActive:
		ps, err := s.st.ListPlacements(ctx, id)
		if err != nil {
			return err
		}
		failed := false
		for _, p := range ps {
			failed = failed || p.Status == store.PlacementFailed
		}
		if !failed {
			return fmt.Errorf("%w: %s, and every site is active", ErrState, t.Status)
		}
		keepStatus = true
	default:
		return fmt.Errorf("%w: %s", ErrState, t.Status)
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	if !keepStatus {
		t.Status, t.LastOp = store.StatusCreating, store.OpProvision
		if err := s.st.UpdateTenant(ctx, t); err != nil {
			s.release(id)
			return err
		}
	}
	s.background(id, store.OpProvision, func(ctx context.Context) error { return s.provision(ctx, id, keepStatus) })
	return nil
}

// RecoverInterrupted marks every tenant left in creating or deleting by a
// previous process (a crash, a kill at shutdown) as failed, so the
// operator sees it and can retry or delete. Called once at start, before
// any operation runs. It returns the number of rows changed.
func (s *Service) RecoverInterrupted(ctx context.Context) (int, error) {
	ts, err := s.st.ListTenants(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range ts {
		t := &ts[i]
		if t.Status != store.StatusCreating && t.Status != store.StatusDeleting {
			continue
		}
		if s.isRunning(t.ID) {
			continue
		}
		t.Status = store.StatusFailed
		t.LastError = fmt.Sprintf("%s interrupted by a restart; retry (provision) or delete", t.LastOp)
		if err := s.st.UpdateTenant(ctx, t); err != nil {
			return n, err
		}
		s.log.Warn("tenant operation interrupted by a restart", "tenant", t.ID, "op", t.LastOp)
		n++
	}
	// Placements left creating or removing (a site added or removed when
	// the process stopped) are failed too, whatever their tenant's status.
	for i := range ts {
		if s.isRunning(ts[i].ID) {
			continue
		}
		ps, err := s.st.ListPlacements(ctx, ts[i].ID)
		if err != nil {
			return n, err
		}
		for j := range ps {
			p := &ps[j]
			if p.Status != store.PlacementCreating && p.Status != store.PlacementRemoving {
				continue
			}
			p.Status, p.LastError = store.PlacementFailed, "interrupted by a restart; retry or remove the site again"
			if err := s.st.UpdatePlacement(ctx, p); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (s *Service) isRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// Delete removes the tenant's access objects in the background, the
// project too when deleteProject is set, then the row.
func (s *Service) Delete(ctx context.Context, id string, deleteProject bool) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	t.Status, t.LastOp = store.StatusDeleting, store.OpDelete
	if err := s.st.UpdateTenant(ctx, t); err != nil {
		s.release(id)
		return err
	}
	s.background(id, store.OpDelete, func(ctx context.Context) error {
		defer s.changed(id)
		return s.remove(ctx, id, deleteProject)
	})
	return nil
}

// Disable disables every Keycloak user of the tenant group: no login, no
// SAML assertion, no access. The Prism Central objects stay.
func (s *Service) Disable(ctx context.Context, id string) error {
	return s.setEnabled(ctx, id, false)
}

// Enable reverses Disable.
func (s *Service) Enable(ctx context.Context, id string) error {
	return s.setEnabled(ctx, id, true)
}

func (s *Service) setEnabled(ctx context.Context, id string, enabled bool) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	want, next := store.StatusActive, store.StatusDisabled
	if enabled {
		want, next = store.StatusDisabled, store.StatusActive
	}
	if t.Status != want {
		return fmt.Errorf("%w: %s", ErrState, t.Status)
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	defer s.release(id)
	members, err := s.kc.GroupMembers(ctx, t.KeycloakGroupID)
	if err != nil {
		return err
	}
	for _, u := range members {
		if err := s.kc.SetUserEnabled(ctx, u.ID, enabled); err != nil {
			return fmt.Errorf("user %s: %w", u.Email, err)
		}
	}
	t.Status = next
	s.log.Info("tenant users enabled flag set", "tenant", id, "enabled", enabled, "users", len(members))
	return s.st.UpdateTenant(ctx, t)
}

// CreateUser creates a Keycloak user in the tenant group with a temporary
// password that is returned once and never stored.
func (s *Service) CreateUser(ctx context.Context, id, email, firstName, lastName string) (string, error) {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return "", err
	}
	if t.Status != store.StatusActive {
		return "", fmt.Errorf("%w: %s", ErrState, t.Status)
	}
	if !s.acquire(id) {
		return "", ErrBusy
	}
	defer s.release(id)
	if _, err := s.kc.FindUserByEmail(ctx, email); err == nil {
		return "", ErrExists
	} else if !errors.Is(err, keycloak.ErrNotFound) {
		return "", err
	}
	pw, err := temporaryPassword()
	if err != nil {
		return "", err
	}
	u, err := s.kc.CreateUser(ctx, keycloak.User{Email: email, FirstName: firstName, LastName: lastName}, pw)
	if err != nil {
		return "", err
	}
	if err := s.kc.AddUserToGroup(ctx, u.ID, t.KeycloakGroupID); err != nil {
		return "", err
	}
	s.log.Info("tenant user created", "tenant", id, "user", email)
	return pw, nil
}

// Visibility is the result of the static visibility check.
type Visibility struct {
	OK       bool     `json:"ok"`
	Findings []string `json:"findings"`
}
