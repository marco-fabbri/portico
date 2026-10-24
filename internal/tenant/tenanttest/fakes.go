// Package tenanttest holds in-memory fakes of the Prism Central and
// Keycloak clients for the tenant service and API tests. They count
// creates, can fail a named call once, and can block a call so a test can
// observe "busy" behaviour.
package tenanttest

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
)

// Gate lets a test hold a fake call open: the fake sends on Started, then
// waits until Release is closed.
type Gate struct {
	Started chan struct{}
	Release chan struct{}
}

// NewGate returns a gate with buffered Started.
func NewGate() *Gate {
	return &Gate{Started: make(chan struct{}, 1), Release: make(chan struct{})}
}

func (g *Gate) pass(ctx context.Context) error {
	if g == nil {
		return nil
	}
	select {
	case g.Started <- struct{}{}:
	default:
	}
	select {
	case <-g.Release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func notFound(what string) error {
	return &nutanix.APIError{Status: http.StatusNotFound, Message: what + " not found"}
}

// PC is an in-memory Prism Central.
type PC struct {
	mu          sync.Mutex
	Projects    []nutanix.Project
	RGs         []nutanix.ResourceGroup
	IdPs        []nutanix.SAMLIdP
	Groups      []nutanix.UserGroup
	Roles       []nutanix.Role
	Memberships []nutanix.RoleMembership
	Policies    map[string][]string // policy display name → extIds it mentions
	Creates     map[string]int      // method → count
	Calls       int
	FailOnce    map[string]error            // method → error returned once
	Clusters    []nutanix.Cluster           // ListClusters
	Containers  []nutanix.StorageContainer  // ListStorageContainers
	GateCreate  *Gate                       // held by CreateProject when set
	GateDelete  *Gate                       // held by DeleteUserGroup when set
	RGCluster   map[string]string           // resource group extId → the cluster it was created on
	Contents    map[string]nutanix.Contents // project extId → what it holds; empty when absent
	seq         int
	calls       map[string]int // method → calls, successful or not
	failNth     map[string]failAt
	failRun     map[string]failAt // FailNext: left calls still failing, < 0 forever
}

type failAt struct {
	left int
	err  error
}

// FailCall makes the n-th call of method from now (1 is the next one)
// return err.
func (p *PC) FailCall(method string, n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNth == nil {
		p.failNth = map[string]failAt{}
	}
	p.failNth[method] = failAt{left: n, err: err}
}

// FailNext makes the next n calls of method return err; n < 0 means every
// call from now on.
func (p *PC) FailNext(method string, n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failRun == nil {
		p.failRun = map[string]failAt{}
	}
	p.failRun[method] = failAt{left: n, err: err}
}

// NewPC returns a Prism Central with one IdP named idpName, the role
// "Project Admin" and the default project.
func NewPC(idpName string) *PC {
	return &PC{
		Projects:  []nutanix.Project{{ExtID: "00000000-0000-0000-0000-000000000000", ID: "default", Name: "_internal", IsDefault: true}},
		IdPs:      []nutanix.SAMLIdP{{ExtID: "idp-1", Name: idpName}},
		Roles:     []nutanix.Role{{ExtID: "role-pa", DisplayName: "Project Admin"}},
		Policies:  map[string][]string{"Super Admin_acp": {"u-admin"}},
		Creates:   map[string]int{},
		FailOnce:  map[string]error{},
		RGCluster: map[string]string{},
		Contents:  map[string]nutanix.Contents{},
	}
}

func (p *PC) ProjectContents(ctx context.Context, projectExtID string) (nutanix.Contents, error) {
	if err := p.call("ProjectContents"); err != nil {
		return nutanix.Contents{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.Contents[projectExtID]; ok {
		return c, nil
	}
	return nutanix.Contents{Counts: map[string]int{}}, nil
}

// CallCount reports how many times method was called, failures included.
func (p *PC) CallCount(method string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[method]
}

func (p *PC) call(method string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[method]++
	if err, ok := p.FailOnce[method]; ok {
		delete(p.FailOnce, method)
		return err
	}
	if f, ok := p.failRun[method]; ok && f.left != 0 {
		if f.left > 0 {
			f.left--
			p.failRun[method] = f
		}
		return f.err
	}
	if f, ok := p.failNth[method]; ok {
		if f.left--; f.left == 0 {
			delete(p.failNth, method)
			return f.err
		}
		p.failNth[method] = f
	}
	return nil
}

func (p *PC) next(prefix string) string {
	p.seq++
	return fmt.Sprintf("%s-%d", prefix, p.seq)
}

func (p *PC) FindProject(ctx context.Context, name string) (*nutanix.Project, error) {
	if err := p.call("FindProject"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Projects {
		if p.Projects[i].Name == name {
			pr := p.Projects[i]
			return &pr, nil
		}
	}
	return nil, notFound("project " + name)
}

func (p *PC) CreateProject(ctx context.Context, id, name, description string) (*nutanix.Project, error) {
	if err := p.call("CreateProject"); err != nil {
		return nil, err
	}
	if err := p.GateCreate.pass(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateProject"]++
	pr := nutanix.Project{ExtID: p.next("p"), ID: id, Name: name, Description: description}
	p.Projects = append(p.Projects, pr)
	return &pr, nil
}

func (p *PC) DeleteProject(ctx context.Context, extID string) error {
	if err := p.call("DeleteProject"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Projects {
		if p.Projects[i].ExtID == extID {
			p.Projects = append(p.Projects[:i], p.Projects[i+1:]...)
			return nil
		}
	}
	return notFound("project " + extID)
}

func (p *PC) ListResourceGroups(ctx context.Context, projectExtID string) ([]nutanix.ResourceGroup, error) {
	if err := p.call("ListResourceGroups"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []nutanix.ResourceGroup
	for _, rg := range p.RGs {
		if rg.ProjectExtID == projectExtID {
			out = append(out, rg)
		}
	}
	return out, nil
}

func (p *PC) CreateResourceGroup(ctx context.Context, name, projectExtID, clusterExtID, containerExtID string) (*nutanix.ResourceGroup, error) {
	if err := p.call("CreateResourceGroup"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateResourceGroup"]++
	rg := nutanix.ResourceGroup{ExtID: p.next("rg"), Name: name, ProjectExtID: projectExtID}
	p.RGs = append(p.RGs, rg)
	p.RGCluster[rg.ExtID] = clusterExtID
	return &rg, nil
}

func (p *PC) DeleteResourceGroup(ctx context.Context, extID string) error {
	if err := p.call("DeleteResourceGroup"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.RGs {
		if p.RGs[i].ExtID == extID {
			p.RGs = append(p.RGs[:i], p.RGs[i+1:]...)
			return nil
		}
	}
	return notFound("resource group " + extID)
}

func (p *PC) ListSAMLIdPs(ctx context.Context) ([]nutanix.SAMLIdP, error) {
	if err := p.call("ListSAMLIdPs"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]nutanix.SAMLIdP(nil), p.IdPs...), nil
}

func (p *PC) GetSAMLIdP(ctx context.Context, extID string) (*nutanix.SAMLIdP, error) {
	if err := p.call("GetSAMLIdP"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.IdPs {
		if p.IdPs[i].ExtID == extID {
			idp := p.IdPs[i]
			return &idp, nil
		}
	}
	return nil, notFound("idp " + extID)
}

func (p *PC) ShareIdP(ctx context.Context, idpExtID, projectExtID string) error {
	if err := p.call("ShareIdP"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["ShareIdP"]++
	for i := range p.IdPs {
		if p.IdPs[i].ExtID == idpExtID {
			p.IdPs[i].SharedWithProjects = append(p.IdPs[i].SharedWithProjects, projectExtID)
			return nil
		}
	}
	return notFound("idp " + idpExtID)
}

func (p *PC) UnshareIdP(ctx context.Context, idpExtID, projectExtID string) error {
	if err := p.call("UnshareIdP"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.IdPs {
		if p.IdPs[i].ExtID == idpExtID {
			var keep []string
			for _, pr := range p.IdPs[i].SharedWithProjects {
				if pr != projectExtID {
					keep = append(keep, pr)
				}
			}
			p.IdPs[i].SharedWithProjects = keep
			return nil
		}
	}
	return notFound("idp " + idpExtID)
}

func (p *PC) FindUserGroup(ctx context.Context, name string) (*nutanix.UserGroup, error) {
	if err := p.call("FindUserGroup"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Groups {
		if p.Groups[i].Name == name {
			g := p.Groups[i]
			return &g, nil
		}
	}
	return nil, notFound("user group " + name)
}

func (p *PC) ListUserGroups(ctx context.Context) ([]nutanix.UserGroup, error) {
	if err := p.call("ListUserGroups"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]nutanix.UserGroup(nil), p.Groups...), nil
}

func (p *PC) CreateUserGroup(ctx context.Context, name, idpExtID string) (*nutanix.UserGroup, error) {
	if err := p.call("CreateUserGroup"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateUserGroup"]++
	g := nutanix.UserGroup{ExtID: p.next("g"), Name: name, IdPExtID: idpExtID}
	p.Groups = append(p.Groups, g)
	return &g, nil
}

func (p *PC) DeleteUserGroup(ctx context.Context, extID string) error {
	if err := p.call("DeleteUserGroup"); err != nil {
		return err
	}
	if err := p.GateDelete.pass(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Groups {
		if p.Groups[i].ExtID == extID {
			p.Groups = append(p.Groups[:i], p.Groups[i+1:]...)
			return nil
		}
	}
	return notFound("user group " + extID)
}

func (p *PC) FindRole(ctx context.Context, displayName string) (*nutanix.Role, error) {
	if err := p.call("FindRole"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Roles {
		if p.Roles[i].DisplayName == displayName {
			r := p.Roles[i]
			return &r, nil
		}
	}
	return nil, notFound("role " + displayName)
}

func (p *PC) ListRoleMemberships(ctx context.Context) ([]nutanix.RoleMembership, error) {
	if err := p.call("ListRoleMemberships"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]nutanix.RoleMembership(nil), p.Memberships...), nil
}

func (p *PC) CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*nutanix.RoleMembership, error) {
	if err := p.call("CreateRoleMembership"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Creates["CreateRoleMembership"]++
	rm := nutanix.RoleMembership{ExtID: p.next("rm"), IdentityExtID: groupExtID, IdentityType: "GROUP", ProjectExtID: projectExtID, RoleExtID: roleExtID, ScopeTemplateName: nutanix.ScopeTemplate}
	p.Memberships = append(p.Memberships, rm)
	return &rm, nil
}

func (p *PC) DeleteRoleMembership(ctx context.Context, extID string) error {
	if err := p.call("DeleteRoleMembership"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Memberships {
		if p.Memberships[i].ExtID == extID {
			p.Memberships = append(p.Memberships[:i], p.Memberships[i+1:]...)
			return nil
		}
	}
	return notFound("role membership " + extID)
}

func (p *PC) PoliciesMentioning(ctx context.Context, extID string) ([]string, error) {
	if err := p.call("PoliciesMentioning"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for name, ids := range p.Policies {
		for _, id := range ids {
			if id == extID {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

// KC is an in-memory Keycloak realm.
type KC struct {
	mu       sync.Mutex
	Groups   []keycloak.Group
	Users    []keycloak.User
	Members  map[string][]string // group id → user ids
	Creates  map[string]int
	Calls    int
	FailOnce map[string]error
	seq      int
	// LastTemporaryPassword is the password of the last CreateUser call.
	LastTemporaryPassword string
}

// NewKC returns an empty realm.
func NewKC() *KC {
	return &KC{Members: map[string][]string{}, Creates: map[string]int{}, FailOnce: map[string]error{}}
}

func (k *KC) call(method string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls++
	if err, ok := k.FailOnce[method]; ok {
		delete(k.FailOnce, method)
		return err
	}
	return nil
}

func (k *KC) FindGroup(ctx context.Context, name string) (*keycloak.Group, error) {
	if err := k.call("FindGroup"); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.Groups {
		if k.Groups[i].Name == name {
			g := k.Groups[i]
			return &g, nil
		}
	}
	return nil, keycloak.ErrNotFound
}

func (k *KC) CreateGroup(ctx context.Context, name string) (*keycloak.Group, error) {
	if err := k.call("CreateGroup"); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Creates["CreateGroup"]++
	k.seq++
	g := keycloak.Group{ID: fmt.Sprintf("kcg-%d", k.seq), Name: name}
	k.Groups = append(k.Groups, g)
	return &g, nil
}

func (k *KC) DeleteGroup(ctx context.Context, id string) error {
	if err := k.call("DeleteGroup"); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.Groups {
		if k.Groups[i].ID == id {
			k.Groups = append(k.Groups[:i], k.Groups[i+1:]...)
			delete(k.Members, id)
			return nil
		}
	}
	return nil // absent is not an error, like the real client
}

func (k *KC) DeleteUser(ctx context.Context, id string) error {
	if err := k.call("DeleteUser"); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.Users {
		if k.Users[i].ID == id {
			k.Users = append(k.Users[:i], k.Users[i+1:]...)
			break
		}
	}
	for g, ids := range k.Members {
		var keep []string
		for _, u := range ids {
			if u != id {
				keep = append(keep, u)
			}
		}
		k.Members[g] = keep
	}
	return nil
}

func (k *KC) FindUserByEmail(ctx context.Context, email string) (*keycloak.User, error) {
	if err := k.call("FindUserByEmail"); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.Users {
		if k.Users[i].Email == email {
			u := k.Users[i]
			return &u, nil
		}
	}
	return nil, keycloak.ErrNotFound
}

// CreateUser records the user; the temporary password is kept in
// LastTemporaryPassword for assertions and RequiredActions is always
// UPDATE_PASSWORD, as the real client sends.
func (k *KC) CreateUser(ctx context.Context, u keycloak.User, temporaryPassword string) (*keycloak.User, error) {
	if err := k.call("CreateUser"); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Creates["CreateUser"]++
	k.seq++
	u.ID = fmt.Sprintf("kcu-%d", k.seq)
	u.Enabled = true
	k.Users = append(k.Users, u)
	k.LastTemporaryPassword = temporaryPassword
	return &u, nil
}

func (k *KC) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	if err := k.call("AddUserToGroup"); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, id := range k.Members[groupID] {
		if id == userID {
			return nil
		}
	}
	k.Members[groupID] = append(k.Members[groupID], userID)
	return nil
}

func (k *KC) GroupMembers(ctx context.Context, groupID string) ([]keycloak.User, error) {
	if err := k.call("GroupMembers"); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []keycloak.User
	for _, id := range k.Members[groupID] {
		for _, u := range k.Users {
			if u.ID == id {
				out = append(out, u)
			}
		}
	}
	return out, nil
}

func (k *KC) SetUserEnabled(ctx context.Context, userID string, enabled bool) error {
	if err := k.call("SetUserEnabled"); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.Users {
		if k.Users[i].ID == userID {
			k.Users[i].Enabled = enabled
			return nil
		}
	}
	return keycloak.ErrNotFound
}

// UserEnabled reports the enabled flag of a user, false when unknown.
func (k *KC) UserEnabled(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, u := range k.Users {
		if u.ID == id {
			return u.Enabled
		}
	}
	return false
}

func (p *PC) ListClusters(ctx context.Context) ([]nutanix.Cluster, error) {
	if err := p.call("ListClusters"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]nutanix.Cluster(nil), p.Clusters...), nil
}

func (p *PC) ListStorageContainers(ctx context.Context) ([]nutanix.StorageContainer, error) {
	if err := p.call("ListStorageContainers"); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]nutanix.StorageContainer(nil), p.Containers...), nil
}
