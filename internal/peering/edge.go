package peering

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"text/template"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

// ErrEdgeNotOurs: a VM with the edge's name exists that Portico did not
// build (no portico:edge category); Portico neither adopts nor deletes it.
var ErrEdgeNotOurs = errors.New("peering: a VM with the edge's name exists without the portico:edge category")

// The edge VM (design 4.3): 1 vCPU, 1 GiB, the image's disk grown to 10 GiB.
const (
	EdgeVCPUs       = 1
	EdgeMemoryBytes = 1 << 30
	EdgeDiskBytes   = 10 << 30
	EdgeCategoryKey = "portico"
	EdgeCategoryVal = "edge"
)

//go:embed edge_userdata.tmpl
var edgeUserDataTmpl string

var edgeTemplate = template.Must(template.New("edge").Parse(edgeUserDataTmpl))

// EdgeUserData renders the edge router's cloud-config: the address and
// default gateway on eth0, the DNS servers, FRR peering with the BGP
// gateway, forwarding on, nftables accepting BGP from the gateway only.
// No user, no key, no password, no NAT, no static route: the edge's
// only route to the routed prefix comes from BGP (design 4.3).
func EdgeUserData(name string, addr netip.Prefix, gw netip.Addr, dns []netip.Addr, asn int, peer netip.Addr, peerASN int) (string, error) {
	if !addr.IsValid() || !gw.IsValid() || !peer.IsValid() || asn == 0 || peerASN == 0 || name == "" {
		return "", errors.New("peering: the edge needs a name, an address with its prefix, a gateway, a peer and both ASNs")
	}
	servers := make([]string, 0, len(dns))
	for _, d := range dns {
		if d.IsValid() {
			servers = append(servers, d.String())
		}
	}
	var buf bytes.Buffer
	err := edgeTemplate.Execute(&buf, map[string]any{
		"Name": name, "Addr": addr.String(), "IP": addr.Addr().String(), "GW": gw.String(), "DNS": strings.Join(servers, ";"),
		"ASN": asn, "Peer": peer.String(), "PeerASN": peerASN,
	})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// EdgePrismCentral is the part of the Prism Central client Edge uses.
type EdgePrismCentral interface {
	FindImage(ctx context.Context, name string) (*nutanix.Image, error)
	CreateImageFromURL(ctx context.Context, name, url string) (*nutanix.Image, error)
	EnsureCategory(ctx context.Context, key, value string) (string, error)
	ListVMs(ctx context.Context) ([]nutanix.VM, error)
	CreateVM(ctx context.Context, s nutanix.VMSpec) (*nutanix.VM, error)
	PowerVM(ctx context.Context, extID string, on bool) error
}

// Edge builds the edge router of a site from the release's image
// (design 4.3 and 4.4). Version is the appliance's; ImageURL overrides
// the release asset's URL (a mirror, a dark site).
type Edge struct {
	PC       EdgePrismCentral
	Version  string
	ImageURL string
}

// EdgeImageName is the image's name in the site's image service.
func EdgeImageName(version string) string { return "portico-edge-" + version }

// DefaultEdgeImageURL is the release asset of the edge image.
func DefaultEdgeImageURL(version string) string {
	return fmt.Sprintf("https://github.com/marco-fabbri/portico/releases/download/v%s/portico-edge-%s.qcow2", version, version)
}

// Build makes the site's edge VM exist, powered on: the image found by
// name or imported from the URL, the category ensured, a VM of the
// edge's name adopted only when it carries the category, else created
// on the VLAN subnet with the cloud-config of EdgeUserData.
func (e Edge) Build(ctx context.Context, site settings.Site, p settings.SitePeering) (string, error) {
	if e.PC == nil {
		return "", errors.New("peering: no Prism Central client for the edge")
	}
	n := namesOf(site)
	vms, err := e.PC.ListVMs(ctx)
	if err != nil {
		return "", fmt.Errorf("the site's VMs: %w", err)
	}
	for _, vm := range vms {
		if vm.Name != n.edge {
			continue
		}
		if !hasCategory(vm, EdgeCategoryKey+":"+EdgeCategoryVal) {
			return "", ErrEdgeNotOurs
		}
		if !vm.PowerOn {
			if err := e.PC.PowerVM(ctx, vm.ExtID, true); err != nil {
				return "", fmt.Errorf("powering the edge router on: %w", err)
			}
		}
		return vm.ExtID, nil
	}
	imageName := EdgeImageName(e.Version)
	img, err := e.PC.FindImage(ctx, imageName)
	if err != nil {
		if !nutanix.IsNotFound(err) {
			return "", fmt.Errorf("the edge image: %w", err)
		}
		url := e.ImageURL
		if url == "" {
			url = DefaultEdgeImageURL(e.Version)
		}
		if img, err = e.PC.CreateImageFromURL(ctx, imageName, url); err != nil {
			return "", fmt.Errorf("importing the edge image %s from %s: %w", imageName, url, err)
		}
	}
	category, err := e.PC.EnsureCategory(ctx, EdgeCategoryKey, EdgeCategoryVal)
	if err != nil {
		return "", fmt.Errorf("the category %s:%s: %w", EdgeCategoryKey, EdgeCategoryVal, err)
	}
	var dns []netip.Addr
	for _, d := range site.Network.DNS {
		if a, err := netip.ParseAddr(d); err == nil {
			dns = append(dns, a)
		}
	}
	addr := netip.PrefixFrom(p.EdgeAddr, p.GatewayAddr.Bits())
	userData, err := EdgeUserData(n.edge, addr, p.GatewayGW, dns, p.EdgeASN, p.GatewayAddr.Addr(), p.GatewayASN)
	if err != nil {
		return "", err
	}
	if p.VLANSubnetExtID == "" {
		return "", errors.New("peering: the edge needs the VLAN subnet the peering found")
	}
	vm, err := e.PC.CreateVM(ctx, nutanix.VMSpec{Name: n.edge, ClusterExtID: site.ClusterExtID, ImageExtID: img.ExtID, SubnetExtID: p.VLANSubnetExtID,
		VCPUs: EdgeVCPUs, MemoryBytes: EdgeMemoryBytes, DiskBytes: EdgeDiskBytes, UserData: userData, CategoryExtID: category})
	if err != nil {
		return "", fmt.Errorf("creating the edge router: %w", err)
	}
	if err := e.PC.PowerVM(ctx, vm.ExtID, true); err != nil {
		return "", fmt.Errorf("powering the edge router on: %w", err)
	}
	return vm.ExtID, nil
}

func hasCategory(vm nutanix.VM, want string) bool {
	for _, c := range vm.Categories {
		if c == want {
			return true
		}
	}
	return false
}
