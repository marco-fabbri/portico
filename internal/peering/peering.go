// Package peering builds and maintains the BGP peering of a site's
// transit VPC (prefix routing design, 2026-11-01): the local BGP gateway,
// the remote gateway standing for the peer (the MSP's router or the edge
// router Portico builds), the session between them, and the external
// subnet's gateway when the edge is the peer.
package peering

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

// SystemProject is Prism Central's `_internal` project, where the peering's
// objects live: a local gateway created without a project cannot read back
// its own image (write-up 37).
const SystemProject = nutanix.SystemProject

// The waits: the local gateway reports DOWN for about a minute after its VM
// is on and its first create downloads the image (write-up 37); a session
// through a fresh edge is UP about 2.5 min after the VM is on (write-up 38).
var (
	Poll         = 20 * time.Second
	GatewayBound = 10 * time.Minute
	SessionBound = 10 * time.Minute
)

// PrismCentral is the part of the Prism Central client the service uses.
type PrismCentral interface {
	ListGateways(ctx context.Context) ([]nutanix.Gateway, error)
	GetGateway(ctx context.Context, extID string) (*nutanix.Gateway, error)
	CreateLocalBGPGateway(ctx context.Context, name, projectExtID, clusterExtID, subnetExtID string, addr netip.Prefix, gw netip.Addr, asn int, vpcExtID string) (*nutanix.Gateway, error)
	CreateRemoteBGPGateway(ctx context.Context, name, projectExtID string, addr netip.Addr, asn int) (*nutanix.Gateway, error)
	UpdateRemoteGateway(ctx context.Context, extID string, addr netip.Addr, asn int) error
	DeleteGateway(ctx context.Context, extID string) error
	ListBGPSessions(ctx context.Context) ([]nutanix.BGPSession, error)
	GetBGPSession(ctx context.Context, extID string) (*nutanix.BGPSession, error)
	CreateBGPSession(ctx context.Context, name, projectExtID, localExtID, remoteExtID string, localAddr netip.Addr) (*nutanix.BGPSession, error)
	DeleteBGPSession(ctx context.Context, extID string) error
	BGPRoutes(ctx context.Context, sessionExtID string) ([]nutanix.BGPRoute, error)
	GetVM(ctx context.Context, extID string) (*nutanix.VM, error)
	PowerVM(ctx context.Context, extID string, on bool) error
	DeleteVM(ctx context.Context, extID string) error
	GetVPC(ctx context.Context, extID string) (*nutanix.VPC, error)
	GetSubnet(ctx context.Context, extID string) (*nutanix.Subnet, error)
	ListSubnets(ctx context.Context) ([]nutanix.Subnet, error)
	CreateVLANSubnet(ctx context.Context, name, clusterExtID string, vlanID int, virtualSwitchExtID string) (*nutanix.Subnet, error)
	DeleteSubnet(ctx context.Context, extID string) error
	UpdateSubnetGateway(ctx context.Context, extID string, gw netip.Addr) error
}

// EdgeBuilder creates the edge router VM of a site (design 4.3) and
// returns its extId, powered on.
type EdgeBuilder interface {
	Build(ctx context.Context, site settings.Site, p settings.SitePeering) (vmExtID string, err error)
}

// Service runs the peering's operations on one site. save stores the
// peering after every change of what Portico built, so a run that stops
// loses no extId.
type Service struct {
	pc   PrismCentral
	log  *slog.Logger
	save func(context.Context, settings.SitePeering) error
}

// New returns a service on pc.
func New(pc PrismCentral, log *slog.Logger, save func(context.Context, settings.SitePeering) error) *Service {
	if log == nil {
		log = slog.Default()
	}
	if save == nil {
		save = func(context.Context, settings.SitePeering) error { return nil }
	}
	return &Service{pc: pc, log: log, save: save}
}

// Names of the objects Portico builds in a site (design 4.1).
type names struct{ local, remote, session, vlan, edge string }

func namesOf(site settings.Site) names {
	base := "portico-" + strings.ToLower(site.Code)
	return names{local: base + "-bgp", remote: base + "-peer", session: base + "-peering", vlan: base + "-vlan", edge: base + "-edge"}
}

// peer is the address and ASN the remote gateway stands for.
func peer(p settings.SitePeering) (netip.Addr, int) {
	if p.KindOrStatic() == settings.PeerEdge {
		return p.EdgeAddr, p.EdgeASN
	}
	return p.RouterAddr, p.RouterASN
}

// Refresh reads the peering's state from Prism Central: up when the
// local gateway and the session are UP, down with the message otherwise,
// none when nothing is built.
func (s *Service) Refresh(ctx context.Context, p settings.SitePeering) (settings.SitePeering, error) {
	p.StateAt = time.Now().UTC()
	if p.LocalGatewayExtID == "" && p.SessionExtID == "" {
		p.State, p.Error = settings.PeeringNone, ""
		return p, nil
	}
	state, msg, err := s.read(ctx, p)
	if err != nil {
		return p, err
	}
	p.State, p.Error = state, msg
	return p, nil
}

// read is Refresh without the timestamp: the state and its message.
func (s *Service) read(ctx context.Context, p settings.SitePeering) (string, string, error) {
	if p.LocalGatewayExtID != "" {
		g, err := s.pc.GetGateway(ctx, p.LocalGatewayExtID)
		if err != nil {
			if nutanix.IsNotFound(err) {
				return settings.PeeringDown, "The BGP gateway is gone: Repair builds it again", nil
			}
			return "", "", fmt.Errorf("the BGP gateway: %w", err)
		}
		if g.State != "UP" {
			return settings.PeeringDown, describe("The BGP gateway is "+orUnknown(g.State), g.Message), nil
		}
	}
	if p.SessionExtID == "" {
		return settings.PeeringDown, "The BGP session is not built yet", nil
	}
	sess, err := s.pc.GetBGPSession(ctx, p.SessionExtID)
	if err != nil {
		if nutanix.IsNotFound(err) {
			return settings.PeeringDown, "The BGP session is gone: Repair builds it again", nil
		}
		return "", "", fmt.Errorf("the BGP session: %w", err)
	}
	if sess.State != "UP" {
		return settings.PeeringDown, describe("The BGP session is "+orUnknown(sess.State), sess.Message), nil
	}
	return settings.PeeringUp, "", nil
}

func orUnknown(state string) string {
	if state == "" {
		return "not reported yet"
	}
	return state
}

func describe(what, msg string) string {
	if msg == "" {
		return what
	}
	return what + ": " + msg
}

// waitGateway polls the local gateway until it is UP, for GatewayBound.
func (s *Service) waitGateway(ctx context.Context, extID string) error {
	deadline := time.Now().Add(GatewayBound)
	var last *nutanix.Gateway
	transient := 0
	for {
		g, err := s.pc.GetGateway(ctx, extID)
		if err != nil {
			// A transient answer is not the gateway's state: the wait
			// goes on (write-up 39: 504 waves), within the bound.
			if transient++; !transientRead(err) || transient > transientReadLimit || time.Now().After(deadline) {
				return err
			}
			if err := sleep(ctx, Poll); err != nil {
				return err
			}
			continue
		}
		transient = 0
		if g.State == "UP" {
			return nil
		}
		last = g
		if time.Now().After(deadline) {
			break
		}
		if err := sleep(ctx, Poll); err != nil {
			return err
		}
	}
	return fmt.Errorf("the BGP gateway is not UP after %s (%s)", GatewayBound, describe(orUnknown(last.State), last.Message))
}

// waitSession polls the session until it is UP, for SessionBound; it
// returns whether it got there and, when not, the last message.
func (s *Service) waitSession(ctx context.Context, extID string) (bool, string, error) {
	deadline := time.Now().Add(SessionBound)
	var last *nutanix.BGPSession
	transient := 0
	for {
		sess, err := s.pc.GetBGPSession(ctx, extID)
		if err != nil {
			if transient++; !transientRead(err) || transient > transientReadLimit || time.Now().After(deadline) {
				return false, "", err
			}
			if err := sleep(ctx, Poll); err != nil {
				return false, "", err
			}
			continue
		}
		transient = 0
		if sess.State == "UP" {
			return true, "", nil
		}
		last = sess
		if time.Now().After(deadline) {
			break
		}
		if err := sleep(ctx, Poll); err != nil {
			return false, "", err
		}
	}
	return false, describe("The BGP session is "+orUnknown(last.State)+" after "+SessionBound.String(), last.Message), nil
}

// transientReadLimit is how many transient answers in a row a wait takes
// before it gives up; the wait's own bound ends first with the 20 s poll
// (write-up 39 measured 504 waves of two to four minutes).
const transientReadLimit = 60

// transientRead reports a read answer the next poll may not get: the
// gateway's 500/502/503 (nutanix.IsTransient) and its 504 "upstream
// request timeout". A task's own failure is not one.
func transientRead(err error) bool {
	var apiErr *nutanix.APIError
	return nutanix.IsTransient(err) || (errors.As(err, &apiErr) && !apiErr.Task && apiErr.Status == 504)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// run is one operation on a site's peering: its steps, the peering as
// they change it, and the first failure.
type run struct {
	s     *Service
	ctx   context.Context
	site  settings.Site
	names names
	p     settings.SitePeering
	ext   *nutanix.Subnet // the transit VPC's routed external subnet
	err   error
}

func (s *Service) start(ctx context.Context, site settings.Site, p settings.SitePeering, state string) *run {
	r := &run{s: s, ctx: ctx, site: site, names: namesOf(site), p: p}
	r.p.State, r.p.Error = state, ""
	r.store()
	return r
}

// store saves the peering as it is; a save that fails is logged, the run
// goes on (Prism Central holds the truth, the next Refresh reads it).
func (r *run) store() {
	if err := r.s.save(r.ctx, r.p); err != nil {
		r.s.log.Warn("peering: save failed", "site", r.site.ID, "err", err)
	}
}

// step runs fn unless a step already failed; a failure sets the peering
// failed with "<step>: <reason>" and stops the run.
func (r *run) step(name string, fn func() error) bool {
	if r.err != nil {
		return false
	}
	if err := fn(); err != nil {
		r.err = fmt.Errorf("%s: %w", name, err)
		r.p.State, r.p.Error = settings.PeeringFailed, r.err.Error()
		r.p.StateAt = time.Now().UTC()
		r.s.log.Warn("peering: step failed", "site", r.site.ID, "step", name, "err", err)
		r.store()
		return false
	}
	r.s.log.Info("peering: step done", "site", r.site.ID, "step", name)
	return true
}

// finish stamps and saves the peering, and returns it with the run's error.
func (r *run) finish() (settings.SitePeering, error) {
	if r.err == nil {
		r.p.StateAt = time.Now().UTC()
		r.store()
	}
	return r.p, r.err
}

// settle reads the session's state (after a wait when wait is true):
// up sets the state up, otherwise down with the message. Not a step: a
// session that is not UP is a state the card shows, not a failed run.
func (r *run) settle(wait bool) bool {
	if r.err != nil {
		return false
	}
	if !wait {
		state, msg, err := r.s.read(r.ctx, r.p)
		if err != nil {
			r.step("read-back", func() error { return err })
			return false
		}
		r.p.State, r.p.Error = state, msg
		return state == settings.PeeringUp
	}
	up, msg, err := r.s.waitSession(r.ctx, r.p.SessionExtID)
	if err != nil {
		r.step("session", func() error { return err })
		return false
	}
	if up {
		r.p.State, r.p.Error = settings.PeeringUp, ""
	} else {
		r.p.State, r.p.Error = settings.PeeringDown, msg
	}
	return up
}
