package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

const (
	networkingRole   = "Portico Networking"
	projectAdminRole = "Project Admin"
)

// networkingEntityTypes are the entity filters of the networking policy:
// every entity of each type, since a narrower filter is not measured
// (tenant networking design 6.1).
var networkingEntityTypes = []string{"subnet", "floating_ip", "vpc", "vpc_route_table"}

// tenantWithout are the operations Portico Tenant leaves out of Project
// Admin's: the tenant's floating IPs are Portico's allowance (design 4.3).
var tenantWithout = []string{"Create_Floating_Ip", "Delete_Floating_Ip"}

// networkingPolicyName is the policy binding the networking role to the
// service account name.
func networkingPolicyName(account string) string { return networkingRole + "_" + account }

// rightsRefusal says which operation a refused call needed, or what Prism
// Central answered.
func rightsRefusal(target, what, op string, err error) string {
	var apiErr *nutanix.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		return fmt.Sprintf("%s: Prism Central refused it (%d); the account needs %s", what, apiErr.Status, op)
	}
	return what + ": " + prismError(target, err)
}

// updateRefused is an update Prism Central turned down on its content, not
// on the account's rights (seen: 400 on the lab role, write-up 34).
func updateRefused(err error) bool {
	var apiErr *nutanix.APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 &&
		apiErr.Status != http.StatusUnauthorized && apiErr.Status != http.StatusForbidden && apiErr.Status != http.StatusNotFound
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// networkingRights makes the two roles of tenant ready networks (design
// 4.3 and 4.5): Portico Networking, bound to the service account by its
// policy, and Portico Tenant, Project Admin without the floating IP
// creation and deletion. Each is created, or given its operations when
// they differ. The status is kept when nothing changed.
func (b *Bootstrapper) networkingRights(ctx context.Context, pc *nutanix.Client, target, account, saExtID string) (status, msg string) {
	names := append(slices.Clone(NetworkingOperations), tenantWithout...)
	ids, err := pc.OperationExtIDs(ctx, names)
	if err != nil {
		return "failed", "the operations could not be read: " + prismError(target, err)
	}
	var netOps []string
	for _, n := range NetworkingOperations {
		id, ok := ids[n]
		if !ok {
			return "failed", "this Prism Central has no operation " + n
		}
		netOps = append(netOps, id)
	}
	var done []string
	role, change, msg := b.networkingRole(ctx, pc, target, account, netOps)
	if msg != "" {
		return "failed", msg
	}
	if change != "" {
		done = append(done, change)
	}
	change, msg = b.networkingPolicy(ctx, pc, target, account, role, saExtID)
	if msg != "" {
		return "failed", msg
	}
	if change != "" {
		done = append(done, change)
	}
	change, note, msg := b.tenantRole(ctx, pc, target, []string{ids[tenantWithout[0]], ids[tenantWithout[1]]})
	if msg != "" {
		return "failed", msg
	}
	if change != "" {
		done = append(done, change)
	}
	if len(done) == 0 && note == "" {
		return "kept", fmt.Sprintf("roles %s and %s and policy %s in place", networkingRole, DefaultTenantRole, networkingPolicyName(account))
	}
	if len(done) == 0 {
		return "ok", note
	}
	msg = strings.Join(done, ", ")
	if note != "" {
		msg += "; " + note
	}
	return "ok", msg
}

// networkingRole finds or makes Portico Networking with ops. A refused
// update replaces the role: only Portico's policy names it, and that
// policy goes first.
func (b *Bootstrapper) networkingRole(ctx context.Context, pc *nutanix.Client, target, account string, ops []string) (*nutanix.RoleDetail, string, string) {
	found, err := pc.FindRole(ctx, networkingRole)
	if nutanix.IsNotFound(err) {
		role, err := pc.CreateRole(ctx, networkingRole, "Portico: tenant subnets in the transit VPC, sharing, floating IPs, default routes", ops)
		if err != nil {
			return nil, "", rightsRefusal(target, "role "+networkingRole, "Create_Role", err)
		}
		b.log.Info("site setup: role created", "role", networkingRole)
		return role, "role " + networkingRole + " created", ""
	}
	if err != nil {
		return nil, "", rightsRefusal(target, "role "+networkingRole, "View_Role", err)
	}
	role, err := pc.GetRole(ctx, found.ExtID)
	if err != nil {
		return nil, "", rightsRefusal(target, "role "+networkingRole, "View_Role", err)
	}
	if sameSet(role.Operations, ops) {
		return role, "", ""
	}
	err = pc.UpdateRoleOperations(ctx, role.ExtID, ops)
	if err == nil {
		b.log.Info("site setup: role operations updated", "role", networkingRole, "operations", len(ops))
		return role, fmt.Sprintf("role %s given %d operations", networkingRole, len(ops)), ""
	}
	if !updateRefused(err) {
		return nil, "", rightsRefusal(target, "role "+networkingRole, "Update_Role", err)
	}
	b.log.Warn("site setup: role update refused, the role is made again", "role", networkingRole, "err", err.Error())
	if pol, err := pc.FindAuthorizationPolicy(ctx, networkingPolicyName(account)); err == nil {
		if err := pc.DeleteAuthorizationPolicy(ctx, pol.ExtID); err != nil && !nutanix.IsNotFound(err) {
			return nil, "", rightsRefusal(target, "policy "+pol.DisplayName, "Delete_Authorization_Policy", err)
		}
	} else if !nutanix.IsNotFound(err) {
		return nil, "", rightsRefusal(target, "policy "+networkingPolicyName(account), "View_Authorization_Policy", err)
	}
	if err := pc.DeleteRole(ctx, role.ExtID); err != nil && !nutanix.IsNotFound(err) {
		return nil, "", rightsRefusal(target, "role "+networkingRole, "Delete_Role", err)
	}
	role, err = pc.CreateRole(ctx, networkingRole, "Portico: tenant subnets in the transit VPC, sharing, floating IPs, default routes", ops)
	if err != nil {
		return nil, "", rightsRefusal(target, "role "+networkingRole, "Create_Role", err)
	}
	return role, "role " + networkingRole + " made again (Prism Central refused its update)", ""
}

// networkingPolicy keeps the policy when it binds role to the service
// account on the networking entity types, else makes it again.
func (b *Bootstrapper) networkingPolicy(ctx context.Context, pc *nutanix.Client, target, account string, role *nutanix.RoleDetail, saExtID string) (string, string) {
	name := networkingPolicyName(account)
	pol, err := pc.FindAuthorizationPolicy(ctx, name)
	switch {
	case err == nil && pol.Role == role.ExtID && pol.Names(saExtID) && sameSet(pol.EntityTypes(), networkingEntityTypes):
		return "", ""
	case err == nil:
		if err := pc.DeleteAuthorizationPolicy(ctx, pol.ExtID); err != nil && !nutanix.IsNotFound(err) {
			return "", rightsRefusal(target, "policy "+name, "Delete_Authorization_Policy", err)
		}
	case !nutanix.IsNotFound(err):
		return "", rightsRefusal(target, "policy "+name, "View_Authorization_Policy", err)
	}
	if _, err := pc.CreateAuthorizationPolicy(ctx, name, "Portico: networking for tenant ready networks", role.ExtID, saExtID, networkingEntityTypes); err != nil {
		return "", rightsRefusal(target, "policy "+name, "Create_Authorization_Policy", err)
	}
	b.log.Info("site setup: policy created", "policy", name)
	return "policy " + name + " created", ""
}

// tenantRole finds or makes Portico Tenant: Project Admin's operations
// without the excluded ones and without the system-only ones. The tenants' role memberships name it, so a
// refused update keeps it as it is and the note says so.
func (b *Bootstrapper) tenantRole(ctx context.Context, pc *nutanix.Client, target string, excluded []string) (change, note, msg string) {
	pa, err := pc.FindRole(ctx, projectAdminRole)
	if err != nil {
		return "", "", rightsRefusal(target, "role "+projectAdminRole, "View_Role", err)
	}
	paRole, err := pc.GetRole(ctx, pa.ExtID)
	if err != nil {
		return "", "", rightsRefusal(target, "role "+projectAdminRole, "View_Role", err)
	}
	// A custom role cannot hold a system-only operation: Project Admin's
	// Create_Role_Membership and Delete_Role_Membership stay out, so a
	// tenant cannot manage the role memberships of its project.
	systemOnly, err := pc.SystemOnlyOperations(ctx)
	if err != nil {
		return "", "", rightsRefusal(target, "operations", "View_Operation", err)
	}
	var ops []string
	for _, o := range paRole.Operations {
		if !slices.Contains(excluded, o) && !slices.Contains(systemOnly, o) {
			ops = append(ops, o)
		}
	}
	found, err := pc.FindRole(ctx, DefaultTenantRole)
	if nutanix.IsNotFound(err) {
		if _, err := pc.CreateRole(ctx, DefaultTenantRole, "Portico: a tenant's role in its project, Project Admin without floating IP creation and deletion and without the system-only operations", ops); err != nil {
			return "", "", rightsRefusal(target, "role "+DefaultTenantRole, "Create_Role", err)
		}
		b.log.Info("site setup: role created", "role", DefaultTenantRole)
		return "role " + DefaultTenantRole + " created", "", ""
	}
	if err != nil {
		return "", "", rightsRefusal(target, "role "+DefaultTenantRole, "View_Role", err)
	}
	role, err := pc.GetRole(ctx, found.ExtID)
	if err != nil {
		return "", "", rightsRefusal(target, "role "+DefaultTenantRole, "View_Role", err)
	}
	if sameSet(role.Operations, ops) {
		return "", "", ""
	}
	err = pc.UpdateRoleOperations(ctx, role.ExtID, ops)
	switch {
	case err == nil:
		b.log.Info("site setup: role operations updated", "role", DefaultTenantRole, "operations", len(ops))
		return fmt.Sprintf("role %s given %d operations", DefaultTenantRole, len(ops)), "", ""
	case updateRefused(err):
		b.log.Warn("site setup: role update refused, the role is kept", "role", DefaultTenantRole, "err", err.Error())
		return "", "role " + DefaultTenantRole + " kept with its earlier operations: Prism Central refused the update (" + prismError(target, err) + ")", ""
	}
	return "", "", rightsRefusal(target, "role "+DefaultTenantRole, "Update_Role", err)
}
