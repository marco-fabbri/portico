package peering

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

func TestEdgeUserDataHasNoUserNoKeyNoNAT(t *testing.T) {
	ud, err := EdgeUserData("portico-s1-edge", netip.MustParsePrefix("10.0.210.194/24"), netip.MustParseAddr("10.0.210.254"),
		[]netip.Addr{netip.MustParseAddr("10.0.210.10"), netip.MustParseAddr("1.1.1.1")}, 65020, netip.MustParseAddr("10.0.210.195"), 65010)
	if err != nil {
		t.Fatalf("EdgeUserData: %v", err)
	}
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatalf("not a cloud-config:\n%s", ud)
	}
	for _, absent := range []string{"users:", "ssh_authorized_keys", "masquerade", "route1=", "ip route", "dnf ", "password", "chpasswd"} {
		if strings.Contains(ud, absent) {
			t.Errorf("user data carries %q:\n%s", absent, ud)
		}
	}
	for _, want := range []string{
		"hostname: portico-s1-edge", "ssh_pwauth: false",
		"address1=10.0.210.194/24,10.0.210.254", "dns=10.0.210.10;1.1.1.1;",
		"router bgp 65020", "bgp router-id 10.0.210.194", "no bgp ebgp-requires-policy",
		"neighbor 10.0.210.195 remote-as 65010", "neighbor 10.0.210.195 soft-reconfiguration inbound",
		"net.ipv4.ip_forward = 1", "send_redirects = 0",
		"policy drop", `ip saddr 10.0.210.195 tcp dport 179 accept`,
		"systemctl restart frr", "systemctl restart nftables", "bgpd=yes",
		"portico-edge ready",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("user data lacks %q:\n%s", want, ud)
		}
	}
	// Port 179 is accepted from the peer only: no other accept of 179.
	if n := len(regexp.MustCompile(`dport 179`).FindAllString(ud, -1)); n != 1 {
		t.Errorf("tcp 179 accepted %d times", n)
	}
	// The static route and DNS are optional.
	ud2, err := EdgeUserData("portico-s1-edge", netip.MustParsePrefix("10.0.210.194/24"), netip.MustParseAddr("10.0.210.254"), nil, 65020, netip.MustParseAddr("10.0.210.195"), 65010)
	if err != nil || strings.Contains(ud2, "dns=") {
		t.Fatalf("without DNS: %v\n%s", err, ud2)
	}
}

// fakeEdgePC is the VM, image and category part of Prism Central.
type fakeEdgePC struct {
	calls  []string
	images map[string]nutanix.Image
	vms    []nutanix.VM
	next   int
}

func (f *fakeEdgePC) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeEdgePC) FindImage(_ context.Context, name string) (*nutanix.Image, error) {
	f.log("FindImage %s", name)
	if img, ok := f.images[name]; ok {
		return &img, nil
	}
	return nil, &nutanix.APIError{Status: 404, Message: name + " not found"}
}

func (f *fakeEdgePC) CreateImageFromURL(_ context.Context, name, url string) (*nutanix.Image, error) {
	f.log("CreateImageFromURL %s %s", name, url)
	f.next++
	img := nutanix.Image{ExtID: fmt.Sprintf("img-%d", f.next), Name: name, SizeBytes: 1 << 30}
	f.images[name] = img
	return &img, nil
}

func (f *fakeEdgePC) EnsureCategory(_ context.Context, key, value string) (string, error) {
	f.log("EnsureCategory %s:%s", key, value)
	return "cat-edge", nil
}

func (f *fakeEdgePC) ListVMs(context.Context) ([]nutanix.VM, error) {
	f.log("ListVMs")
	return append([]nutanix.VM(nil), f.vms...), nil
}

func (f *fakeEdgePC) CreateVM(_ context.Context, s nutanix.VMSpec) (*nutanix.VM, error) {
	f.log("CreateVM %s cluster=%s image=%s subnet=%s vcpus=%d mem=%d disk=%d category=%s ud=%d", s.Name, s.ClusterExtID, s.ImageExtID, s.SubnetExtID, s.VCPUs, s.MemoryBytes, s.DiskBytes, s.CategoryExtID, len(s.UserData))
	f.next++
	vm := nutanix.VM{ExtID: fmt.Sprintf("vm-%d", f.next), Name: s.Name, ClusterExtID: s.ClusterExtID, Categories: []string{"portico:edge"}}
	f.vms = append(f.vms, vm)
	return &vm, nil
}

func (f *fakeEdgePC) PowerVM(_ context.Context, extID string, on bool) error {
	f.log("PowerVM %s on=%v", extID, on)
	for i := range f.vms {
		if f.vms[i].ExtID == extID {
			f.vms[i].PowerOn = on
		}
	}
	return nil
}

func (f *fakeEdgePC) has(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func edgeSite() settings.Site {
	s := testSite()
	s.Network.DNS = []string{"10.0.210.10"}
	return s
}

func edgeInput() settings.SitePeering {
	p := edgePeering()
	p.VLANSubnetExtID = mgmtSubnet
	p.LocalGatewayExtID = "gw-L"
	return p
}

func TestEdgeBuildImportsImageOnce(t *testing.T) {
	f := &fakeEdgePC{images: map[string]nutanix.Image{}}
	e := Edge{PC: f, Version: "0.3.0", ImageURL: "https://example.test/portico-edge-0.3.0.qcow2"}
	id, err := e.Build(context.Background(), edgeSite(), edgeInput())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if id != "vm-2" || !f.has("CreateImageFromURL portico-edge-0.3.0 https://example.test/portico-edge-0.3.0.qcow2") || !f.has("EnsureCategory portico:edge") ||
		!f.has("CreateVM portico-s1-edge cluster=cl-1 image=img-1 subnet="+mgmtSubnet+" vcpus=1 mem=1073741824 disk=10737418240 category=cat-edge") || !f.has("PowerVM vm-2 on=true") {
		t.Fatalf("calls:\n  %s", strings.Join(f.calls, "\n  "))
	}
	// A second site on the same Prism Central finds the image.
	f.calls = nil
	site2 := edgeSite()
	site2.Code = "S2"
	if _, err := e.Build(context.Background(), site2, edgeInput()); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if f.has("CreateImageFromURL") || !f.has("CreateVM portico-s2-edge") {
		t.Fatalf("second build calls:\n  %s", strings.Join(f.calls, "\n  "))
	}
}

func TestEdgeBuildRefusesForeignVM(t *testing.T) {
	f := &fakeEdgePC{images: map[string]nutanix.Image{"portico-edge-0.3.0": {ExtID: "img-1", Name: "portico-edge-0.3.0"}},
		vms: []nutanix.VM{{ExtID: "vm-x", Name: "portico-s1-edge", ClusterExtID: "cl-1", PowerOn: true}}}
	e := Edge{PC: f, Version: "0.3.0"}
	_, err := e.Build(context.Background(), edgeSite(), edgeInput())
	if !errors.Is(err, ErrEdgeNotOurs) {
		t.Fatalf("err = %v", err)
	}
	if f.has("CreateVM") || f.has("PowerVM") {
		t.Fatalf("calls:\n  %s", strings.Join(f.calls, "\n  "))
	}
}

func TestEdgeBuildReusesOurVM(t *testing.T) {
	f := &fakeEdgePC{images: map[string]nutanix.Image{"portico-edge-0.3.0": {ExtID: "img-1", Name: "portico-edge-0.3.0"}},
		vms: []nutanix.VM{{ExtID: "vm-ours", Name: "portico-s1-edge", ClusterExtID: "cl-1", PowerOn: false, Categories: []string{"portico:edge"}}}}
	e := Edge{PC: f, Version: "0.3.0"}
	id, err := e.Build(context.Background(), edgeSite(), edgeInput())
	if err != nil || id != "vm-ours" {
		t.Fatalf("Build = %q, %v", id, err)
	}
	if f.has("CreateVM") || !f.has("PowerVM vm-ours on=true") {
		t.Fatalf("calls:\n  %s", strings.Join(f.calls, "\n  "))
	}
}

func TestEdgeBuildDefaultsTheImageURLToTheRelease(t *testing.T) {
	f := &fakeEdgePC{images: map[string]nutanix.Image{}}
	e := Edge{PC: f, Version: "0.3.0"}
	if _, err := e.Build(context.Background(), edgeSite(), edgeInput()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !f.has("CreateImageFromURL portico-edge-0.3.0 https://github.com/marco-fabbri/portico/releases/download/v0.3.0/portico-edge-0.3.0.qcow2") {
		t.Fatalf("calls:\n  %s", strings.Join(f.calls, "\n  "))
	}
}

var updateFixtures = flag.Bool("update", false, "rewrite the boot test's user data fixture from EdgeUserData")

// The edge image's boot test (image/scripts/edge-boottest.sh) boots the
// image with this fixture on a config drive: the edge on QEMU's user-mode
// network, its peer the host side of that network. The fixture is
// EdgeUserData's output, so the boot test exercises what Portico sends;
// `go test ./internal/peering -run Fixture -update` rewrites it.
func TestEdgeBootTestFixtureIsEdgeUserData(t *testing.T) {
	const path = "../../image/scripts/edge-boottest-user-data.yaml"
	want, err := EdgeUserData("portico-s1-edge", netip.MustParsePrefix("10.0.2.15/24"), netip.MustParseAddr("10.0.2.2"),
		[]netip.Addr{netip.MustParseAddr("10.0.2.3")}, 65020, netip.MustParseAddr("10.0.2.2"), 65010)
	if err != nil {
		t.Fatal(err)
	}
	if *updateFixtures {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s differs from EdgeUserData: run go test ./internal/peering -run Fixture -update", path)
	}
}
