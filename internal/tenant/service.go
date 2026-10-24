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
	"sort"
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
	// ErrSiteIncomplete: the site's setup is broken (sites usability 4.2):
	// no tenant is connected to it until Repair.
	ErrSiteIncomplete = errors.New("tenant: the site's setup is incomplete")
	// ErrHasSites: a tenant is deleted only when it is connected to no
	// site (design sites usability 4.6).
	ErrHasSites = errors.New("tenant: the tenant is still connected to a site; disconnect it from every site first")
	// ErrCount: the project's contents could not be counted, so a
	// disconnect is refused (an unknown is not an empty).
	ErrCount = errors.New("the project's contents could not be counted")
	// ErrPlacementExists: the tenant is already on that site.
	ErrPlacementExists = errors.New("tenant: already placed on this site")
	// ErrNoPlacementTarget: neither the request nor the site names a cluster and a storage container.
	ErrNoPlacementTarget = errors.New("tenant: a cluster and a storage container are needed on this site (the site has no defaults)")
)

// Site is one Prism Central the service places tenants on (design sites
// §4.1), with its client, its SAML identity provider and the default
// placement of new tenants.
type Site struct {
	ID, Code, Name                      string
	URL                                 string // Prism Central's address, for links in the admin UI
	PC                                  PrismCentral
	SAMLIdPName                         string
	SAMLIdPExtID                        string // stored at setup; "" on a site set up before
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
	ListUserGroups(ctx context.Context) ([]nutanix.UserGroup, error)
	CreateUserGroup(ctx context.Context, name, idpExtID string) (*nutanix.UserGroup, error)
	DeleteUserGroup(ctx context.Context, extID string) error
	FindRole(ctx context.Context, displayName string) (*nutanix.Role, error)
	ListRoleMemberships(ctx context.Context) ([]nutanix.RoleMembership, error)
	CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*nutanix.RoleMembership, error)
	DeleteRoleMembership(ctx context.Context, extID string) error
	PoliciesMentioning(ctx context.Context, extID string) ([]string, error)
	ProjectContents(ctx context.Context, projectExtID string) (nutanix.Contents, error)
	ListClusters(ctx context.Context) ([]nutanix.Cluster, error)
	ListStorageContainers(ctx context.Context) ([]nutanix.StorageContainer, error)
}

// NotEmptyError refuses a disconnect: the project still holds what
// Contents names (design sites usability 4.6). Emptying it is the MSP's
// work, in Prism Central.
type NotEmptyError struct {
	Site, Project, URL string
	Contents           nutanix.Contents
}

func (e *NotEmptyError) Error() string {
	return fmt.Sprintf("the project %s on %s still holds %s: empty it in Prism Central first", e.Project, e.Site, e.Contents)
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
	DeleteUser(ctx context.Context, id string) error
}

// Config holds the naming settings of the service; placement lives on
// the sites.
type Config struct {
	ProjectPrefix    string        // project name and id = ProjectPrefix + "-" + slug, on every site
	GroupPrefix      string        // Keycloak and SAML group = GroupPrefix + slug
	RoleName         string        // role bound inside the project, "Project Admin" by default
	OperationTimeout time.Duration // context of a background run
	// RetryPauses are the waits before a step is run again after a
	// transient Prism Central answer (nutanix.IsTransient); its length is
	// the number of repeats. Default 5 s then 15 s.
	RetryPauses []time.Duration
	// NotEmptyBackoff is the first wait before a resource group that Prism
	// Central refused as "not empty" (10018, or the gateway's 504 while
	// its task runs) is deleted again; it doubles up to 2 minutes, within
	// OperationTimeout. Default 30 s.
	NotEmptyBackoff time.Duration
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
	if c.NotEmptyBackoff <= 0 {
		c.NotEmptyBackoff = 30 * time.Second
	}
	if c.RetryPauses == nil {
		c.RetryPauses = []time.Duration{5 * time.Second, 15 * time.Second}
	}
	return c
}

// Service runs tenant operations.
type Service struct {
	st store.Store
	// sites and order change at runtime (SetSites, usability 4.9); read
	// them through lookupSite and Sites. An operation keeps the Site value
	// it read when it started.
	sitesMu sync.RWMutex
	sites   map[string]Site
	order   []string
	gate    func(siteID string) error
	kc      Keycloak
	cfg     Config
	log     *slog.Logger

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
	s := &Service{st: st, kc: kc, cfg: cfg.withDefaults(), log: log, running: map[string]bool{}}
	s.SetSites(sites)
	return s
}

// SetSites replaces the configured sites (sites usability design 4.9).
// Operations already running finish on the sites they read.
func (s *Service) SetSites(sites []Site) {
	m := make(map[string]Site, len(sites))
	order := make([]string, 0, len(sites))
	for _, site := range sites {
		m[site.ID] = site
		order = append(order, site.ID)
	}
	s.sitesMu.Lock()
	s.sites, s.order = m, order
	s.sitesMu.Unlock()
}

// SetSiteGate sets the check a site must pass before a tenant is
// connected to it: the appliance refuses an incomplete site with an
// error wrapping ErrSiteIncomplete.
func (s *Service) SetSiteGate(gate func(siteID string) error) {
	s.sitesMu.Lock()
	s.gate = gate
	s.sitesMu.Unlock()
}

// lookupSite returns a configured site.
func (s *Service) lookupSite(id string) (Site, bool) {
	s.sitesMu.RLock()
	defer s.sitesMu.RUnlock()
	site, ok := s.sites[id]
	return site, ok
}

// Sites returns the configured sites in order.
func (s *Service) Sites() []Site {
	s.sitesMu.RLock()
	defer s.sitesMu.RUnlock()
	out := make([]Site, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.sites[id])
	}
	return out
}

// SiteCode returns the code of a configured site, or the id itself.
func (s *Service) SiteCode(id string) string {
	if site, ok := s.lookupSite(id); ok && site.Code != "" {
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
	site, ok := s.lookupSite(r.Site)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSite, r.Site)
	}
	s.sitesMu.RLock()
	gate := s.gate
	s.sitesMu.RUnlock()
	if gate != nil {
		if err := gate(site.ID); err != nil {
			return nil, err
		}
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
	if reqs == nil {
		reqs = s.DefaultSites()
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

// Suspend closes Portico's route from the tenant to one site, for
// maintenance (design sites usability 4.5). Nothing changes in Prism
// Central: the project, its group and role membership stay and the VMs
// keep running; Resume opens the same project again.
func (s *Service) Suspend(ctx context.Context, id, siteID string) error {
	return s.setPlacementStatus(ctx, id, siteID, store.PlacementActive, store.PlacementSuspended)
}

// Resume reopens a suspended connection; nothing is re-created.
func (s *Service) Resume(ctx context.Context, id, siteID string) error {
	return s.setPlacementStatus(ctx, id, siteID, store.PlacementSuspended, store.PlacementActive)
}

func (s *Service) setPlacementStatus(ctx context.Context, id, siteID string, from, to store.PlacementStatus) error {
	if _, ok := s.lookupSite(siteID); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSite, siteID)
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	defer s.release(id)
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	if p.Status != from {
		return fmt.Errorf("%w: the connection to %s is %s", ErrState, s.SiteCode(siteID), p.Status)
	}
	p.Status, p.LastError = to, ""
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// Contents counts what the tenant's project holds on one site, with the
// service account (write-up 24, item 1).
func (s *Service) Contents(ctx context.Context, id, siteID string) (nutanix.Contents, error) {
	site, err := s.site(siteID)
	if err != nil {
		return nutanix.Contents{}, fmt.Errorf("%w: %q", ErrUnknownSite, siteID)
	}
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return nutanix.Contents{}, err
	}
	if p.ProjectExtID == "" {
		return nutanix.Contents{Counts: map[string]int{}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()
	c, err := site.PC.ProjectContents(ctx, p.ProjectExtID)
	if err != nil {
		return nutanix.Contents{}, fmt.Errorf("%w on %s: %v", ErrCount, site.Code, err)
	}
	return c, nil
}

// Disconnect takes a tenant off one site (design sites usability 4.6).
// It counts what the project holds first and refuses, with a
// *NotEmptyError, when anything is there, or with ErrCount when the
// count fails. With an empty project it removes, in the background, the
// role memberships, the identity provider's sharing, the SAML group, the
// resource group and the project, then the connection. Prism Central's
// own refusal to delete a project that still holds something (a kind
// Portico cannot count) ends the connection remove-failed; RetrySite
// runs the disconnect again. The last site can be disconnected.
func (s *Service) Disconnect(ctx context.Context, id, siteID string) error {
	if _, err := s.st.GetTenant(ctx, id); err != nil {
		return err
	}
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	switch p.Status {
	case store.PlacementActive, store.PlacementSuspended, store.PlacementFailed, store.PlacementRemoveFailed:
	default:
		return fmt.Errorf("%w: the connection to %s is %s", ErrState, s.SiteCode(siteID), p.Status)
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	c, err := s.Contents(ctx, id, siteID)
	if err != nil {
		s.release(id)
		return err
	}
	if !c.Empty() {
		s.release(id)
		site, _ := s.site(siteID)
		return &NotEmptyError{Site: site.Code, Project: p.ProjectID, URL: site.URL, Contents: c}
	}
	p.Status, p.LastError = store.PlacementRemoving, ""
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		s.release(id)
		return err
	}
	s.changed(id)
	s.background(id, "disconnect", func(ctx context.Context) error {
		defer s.changed(id)
		return s.removePlacement(ctx, id, siteID)
	})
	return nil
}

// RetrySite retries one connection only (design sites usability 4.4): a
// failed one is provisioned again, one whose disconnect failed is
// disconnected again (counting first).
func (s *Service) RetrySite(ctx context.Context, id, siteID string) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	p, err := s.st.GetPlacement(ctx, id, siteID)
	if err != nil {
		return err
	}
	switch p.Status {
	case store.PlacementRemoveFailed:
		return s.Disconnect(ctx, id, siteID)
	case store.PlacementFailed:
	default:
		return fmt.Errorf("%w: the connection to %s is %s", ErrState, s.SiteCode(siteID), p.Status)
	}
	keepStatus := true
	switch t.Status {
	case store.StatusActive, store.StatusDisabled:
	case store.StatusFailed:
		if t.LastOp == store.OpDelete {
			return fmt.Errorf("%w: the last operation was a deletion that failed; delete the tenant again", ErrState)
		}
		keepStatus = false
	default:
		return fmt.Errorf("%w: %s", ErrState, t.Status)
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	s.background(id, store.OpProvision, func(ctx context.Context) error { return s.provisionSites(ctx, id, keepStatus, siteID) })
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
			switch p.Status {
			case store.PlacementCreating:
				p.Status, p.LastError = store.PlacementFailed, "provisioning interrupted by a restart; retry"
			case store.PlacementRemoving:
				p.Status, p.LastError = store.PlacementRemoveFailed, "removal interrupted by a restart; remove the site again"
			default:
				continue
			}
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
func (s *Service) Delete(ctx context.Context, id string) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	if !s.acquire(id) {
		return ErrBusy
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		s.release(id)
		return err
	}
	if len(ps) > 0 {
		s.release(id)
		return ErrHasSites
	}
	t.Status, t.LastOp = store.StatusDeleting, store.OpDelete
	if err := s.st.UpdateTenant(ctx, t); err != nil {
		s.release(id)
		return err
	}
	s.background(id, store.OpDelete, func(ctx context.Context) error {
		defer s.changed(id)
		return s.remove(ctx, id)
	})
	return nil
}

// UserCount is the number of users in the tenant's Keycloak group, the
// users Delete removes.
func (s *Service) UserCount(ctx context.Context, id string) (int, error) {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return 0, err
	}
	if t.KeycloakGroupID == "" {
		return 0, nil
	}
	users, err := s.kc.GroupMembers(ctx, t.KeycloakGroupID)
	return len(users), err
}

// DefaultSites is where a tenant goes when a request names no sites: the
// only site when exactly one is configured, otherwise none (the tenant
// is connected later, design sites usability 4.4).
func (s *Service) DefaultSites() []PlacementRequest {
	if sites := s.Sites(); len(sites) == 1 {
		return []PlacementRequest{{Site: sites[0].ID}}
	}
	return []PlacementRequest{}
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

// Target is one choice in the cluster or storage container menu of a
// connection; SiteDefault marks the site's default.
type Target struct {
	ExtID, Name, ClusterName string
	SiteDefault              bool
}

// Targets are the menus offered when a tenant is connected to a site.
type Targets struct {
	Clusters, Containers []Target
}

// targetsTimeout bounds the two list calls behind the menus: a slow
// Prism Central must not hold the admin page.
const targetsTimeout = 10 * time.Second

// Targets reads the site's AOS clusters and their non-system storage
// containers, the site's defaults marked and first. When Prism Central
// cannot be read it returns the defaults alone with the error, so the
// page can still offer them.
func (s *Service) Targets(ctx context.Context, siteID string) (Targets, error) {
	site, ok := s.lookupSite(siteID)
	if !ok {
		return Targets{}, ErrUnknownSite
	}
	defaults := Targets{}
	if site.ClusterExtID != "" {
		defaults.Clusters = []Target{{ExtID: site.ClusterExtID, SiteDefault: true}}
	}
	if site.StorageContainerExtID != "" {
		defaults.Containers = []Target{{ExtID: site.StorageContainerExtID, SiteDefault: true}}
	}
	ctx, cancel := context.WithTimeout(ctx, targetsTimeout)
	defer cancel()
	cls, err := site.PC.ListClusters(ctx)
	if err != nil {
		return defaults, err
	}
	scs, err := site.PC.ListStorageContainers(ctx)
	if err != nil {
		return defaults, err
	}
	var out Targets
	names := map[string]string{}
	for _, cl := range cls {
		if !cl.IsAOS {
			continue
		}
		names[cl.ExtID] = cl.Name
		out.Clusters = append(out.Clusters, Target{ExtID: cl.ExtID, Name: cl.Name, SiteDefault: cl.ExtID == site.ClusterExtID})
	}
	for _, sc := range scs {
		name, aos := names[sc.ClusterExtID]
		if sc.IsSystem || !aos {
			continue
		}
		out.Containers = append(out.Containers, Target{ExtID: sc.ExtID, Name: sc.Name, ClusterName: name, SiteDefault: sc.ExtID == site.StorageContainerExtID})
	}
	defaultFirst := func(ts []Target) {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].SiteDefault && !ts[j].SiteDefault })
	}
	defaultFirst(out.Clusters)
	defaultFirst(out.Containers)
	return out, nil
}
