package tenant

import (
	"context"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// NetworkWatchInterval is how often the stored objects of the ready
// networks are read (my call: the pace of the site trust checks).
const NetworkWatchInterval = 5 * time.Minute

// RunNetworkWatch calls WatchNetworks every interval until ctx ends.
func (s *Service) RunNetworkWatch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.WatchNetworks(ctx)
		}
	}
}

// WatchNetworks reads, by stored extId, what Portico created for every
// ready network (tenant networking design 4.3). One deleted or changed
// in Prism Central turns the state to failed, and Retry network creates
// again what is missing; the watch itself creates nothing and does not
// undo a change. A read that fails for another reason (the site does not
// answer) changes nothing, and a tenant busy with another operation is
// read on the next pass.
func (s *Service) WatchNetworks(ctx context.Context) {
	for _, site := range s.Sites() {
		ps, err := s.st.ListPlacementsBySite(ctx, site.ID)
		if err != nil {
			s.log.Warn("network watch: placements unreadable", "site", site.ID, "err", err.Error())
			continue
		}
		for _, p := range ps {
			if p.NetState != store.NetReady || ctx.Err() != nil {
				continue
			}
			s.watchPlacement(ctx, site, p.TenantID)
		}
	}
}

func (s *Service) watchPlacement(ctx context.Context, site Site, tenantID string) {
	if !s.acquire(tenantID) {
		return
	}
	defer s.release(tenantID)
	// The row again, under the lock: an operation may have just ended.
	p, err := s.st.GetPlacement(ctx, tenantID, site.ID)
	if err != nil || p.NetState != store.NetReady {
		return
	}
	findings, err := networkDrift(ctx, site.PC, p)
	if err != nil {
		s.log.Info("network watch: site not read", "site", site.ID, "tenant", tenantID, "err", err.Error())
		return
	}
	if len(findings) == 0 {
		return
	}
	p.NetState, p.NetError = store.NetFailed, strings.Join(findings, "; ")+": Retry network creates again what is missing"
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		s.log.Error("network watch: state not stored", "site", site.ID, "tenant", tenantID, "err", err.Error())
		return
	}
	s.log.Warn("ready network changed in Prism Central", "site", site.ID, "tenant", tenantID, "found", p.NetError)
	s.changed(tenantID)
}

// networkDrift lists what is deleted or changed among the connection's
// stored objects. Any error other than not found is returned instead:
// nothing is concluded from a site that did not answer.
func networkDrift(ctx context.Context, pc PrismCentral, p *store.Placement) ([]string, error) {
	project := p.ProjectID
	var out []string
	gone := func(what string) { out = append(out, what+" deleted in Prism Central") }
	moved := func(what string) { out = append(out, what+" changed in Prism Central") }

	if p.ExtSubnetExtID != "" {
		switch _, err := pc.GetSubnet(ctx, p.ExtSubnetExtID); {
		case nutanix.IsNotFound(err):
			gone(project + "-ext")
		case err != nil:
			return nil, err
		}
	}
	if p.VPCExtID != "" {
		v, err := pc.GetVPC(ctx, p.VPCExtID)
		switch {
		case nutanix.IsNotFound(err):
			gone(project + "-vpc")
		case err != nil:
			return nil, err
		case v.ProjectExtID != p.ProjectExtID || !vpcUses(v, p.ExtSubnetExtID):
			moved(project + "-vpc")
		}
	}
	if p.SubnetExtID != "" {
		n, err := pc.GetSubnet(ctx, p.SubnetExtID)
		switch {
		case nutanix.IsNotFound(err):
			gone(project + "-net")
		case err != nil:
			return nil, err
		case n.ProjectExtID != p.ProjectExtID || n.VPCExtID != p.VPCExtID:
			moved(project + "-net")
		}
	}
	for _, f := range p.FloatingIPs {
		got, err := pc.GetFloatingIP(ctx, f.ExtID)
		switch {
		case nutanix.IsNotFound(err):
			gone("floating IP " + f.IP)
		case err != nil:
			return nil, err
		// A floating IP read without a project is not taken as moved:
		// whether Prism Central always returns it is not measured.
		case got.ProjectExtID != "" && got.ProjectExtID != p.ProjectExtID || got.ExternalSubnetExtID != p.ExtSubnetExtID:
			moved("floating IP " + f.IP)
		}
	}
	return out, nil
}

// vpcUses reports whether the VPC goes out through the external subnet.
func vpcUses(v *nutanix.VPC, extSubnetExtID string) bool {
	for _, e := range v.External {
		if e.SubnetExtID == extSubnetExtID {
			return true
		}
	}
	return false
}
