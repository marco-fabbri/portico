package nutanix

import (
	"context"
	"encoding/base64"
	"net/netip"
	"testing"
)

func fakeVM(extID, name, power string, categories ...string) map[string]any {
	cats := make([]any, 0, len(categories))
	for _, c := range categories {
		cats = append(cats, map[string]any{"extId": c})
	}
	return map[string]any{"extId": extID, "name": name, "cluster": map[string]any{"extId": "cl-1"}, "powerState": power, "categories": cats}
}

func TestCreateVMBodyHasCloudInitAndCategory(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathVMs] = []any{fakeVM("vm-1", "portico-mi1-edge", "OFF", "cat-1")}
	f.lists[PathCategories] = []any{map[string]any{"extId": "cat-1", "key": "portico", "value": "edge"}}
	c := newClient(t, f, nil)
	spec := VMSpec{Name: "portico-mi1-edge", ClusterExtID: "cl-1", ImageExtID: "img-1", SubnetExtID: "sub-1",
		VCPUs: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30, UserData: "#cloud-config\nhostname: edge\n", CategoryExtID: "cat-1"}
	vm, err := c.CreateVM(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if vm.ExtID != "vm-1" || vm.PowerOn || vm.ClusterExtID != "cl-1" || len(vm.Categories) != 1 || vm.Categories[0] != "portico:edge" {
		t.Fatalf("read back = %+v", vm)
	}
	b := find(f.requests(), "POST", PathVMs).Body
	if b["name"] != "portico-mi1-edge" || b["numSockets"] != float64(1) || b["numCoresPerSocket"] != float64(2) || b["memorySizeBytes"] != float64(1<<30) ||
		b["cluster"].(map[string]any)["extId"] != "cl-1" || b["bootConfig"].(map[string]any)["$objectType"] != "vmm.v4.ahv.config.LegacyBoot" {
		t.Fatalf("POST body = %v", b)
	}
	disk := b["disks"].([]any)[0].(map[string]any)["backingInfo"].(map[string]any)
	if disk["diskSizeBytes"] != float64(10<<30) || disk["dataSource"].(map[string]any)["reference"].(map[string]any)["imageExtId"] != "img-1" {
		t.Fatalf("disk = %v", disk)
	}
	nic := b["nics"].([]any)[0].(map[string]any)["networkInfo"].(map[string]any)
	if nic["subnet"].(map[string]any)["extId"] != "sub-1" || nic["nicType"] != "NORMAL_NIC" {
		t.Fatalf("nic = %v", nic)
	}
	gc := b["guestCustomization"].(map[string]any)["config"].(map[string]any)
	ud, _ := base64.StdEncoding.DecodeString(gc["cloudInitScript"].(map[string]any)["value"].(string))
	if gc["datasourceType"] != "CONFIG_DRIVE_V2" || gc["$objectType"] != "vmm.v4.ahv.config.CloudInit" || string(ud) != spec.UserData {
		t.Fatalf("guestCustomization = %v", gc)
	}
	if cats := b["categories"].([]any); len(cats) != 1 || cats[0].(map[string]any)["extId"] != "cat-1" {
		t.Fatalf("categories = %v", b["categories"])
	}
}

func TestCreateVMWithoutCategoryOmitsIt(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathVMs] = []any{fakeVM("vm-1", "portico-mi1-edge", "OFF")}
	c := newClient(t, f, nil)
	if _, err := c.CreateVM(context.Background(), VMSpec{Name: "portico-mi1-edge", ClusterExtID: "cl-1", ImageExtID: "img-1", SubnetExtID: "sub-1", VCPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 10 << 30}); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	b := find(f.requests(), "POST", PathVMs).Body
	if _, ok := b["categories"]; ok {
		t.Fatalf("categories sent without one: %v", b)
	}
	if _, ok := b["guestCustomization"]; ok {
		t.Fatalf("guestCustomization sent without user data: %v", b)
	}
}

func TestPowerVMOffIsAnAction(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathVMs+"/vm-1"] = fakeVM("vm-1", "portico-mi1-edge", "ON")
	c := newClient(t, f, nil)
	if err := c.PowerVM(context.Background(), "vm-1", false); err != nil {
		t.Fatalf("PowerVM: %v", err)
	}
	post := find(f.requests(), "POST", PathVMs+"/vm-1/$actions/power-off")
	if post == nil || post.Header.Get("If-Match") != f.etag {
		t.Fatalf("power-off = %+v", post)
	}
	if f.polls == 0 {
		t.Fatal("the action's task was not waited for")
	}
	if err := c.PowerVM(context.Background(), "vm-1", true); err != nil {
		t.Fatalf("PowerVM on: %v", err)
	}
	if find(f.requests(), "POST", PathVMs+"/vm-1/$actions/power-on") == nil {
		t.Fatal("no power-on action")
	}
}

func TestDeleteVMSendsETag(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathVMs+"/vm-1"] = fakeVM("vm-1", "portico-mi1-edge", "OFF")
	c := newClient(t, f, nil)
	if err := c.DeleteVM(context.Background(), "vm-1"); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	if del := find(f.requests(), "DELETE", PathVMs+"/vm-1"); del == nil || del.Header.Get("If-Match") != f.etag {
		t.Fatalf("delete = %+v", del)
	}
}

func TestEnsureCategoryCreatesOnce(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathCategories] = []any{map[string]any{"extId": "cat-0", "key": "portico", "value": "other"}}
	c := newClient(t, f, nil)
	id, err := c.EnsureCategory(context.Background(), "portico", "edge")
	if err != nil {
		t.Fatalf("EnsureCategory: %v", err)
	}
	post := find(f.requests(), "POST", PathCategories)
	if id != "cat-new" || post == nil || post.Body["key"] != "portico" || post.Body["value"] != "edge" {
		t.Fatalf("id = %q, post = %+v", id, post)
	}
	f.lists[PathCategories] = []any{map[string]any{"extId": "cat-new", "key": "portico", "value": "edge"}}
	id, err = c.EnsureCategory(context.Background(), "portico", "edge")
	if err != nil || id != "cat-new" {
		t.Fatalf("second EnsureCategory = %q, %v", id, err)
	}
	posts := 0
	for _, r := range f.requests() {
		if r.Method == "POST" && r.Path == PathCategories {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("category posted %d times", posts)
	}
}

func TestCreateImageFromURLSource(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathImages] = []any{map[string]any{"extId": "img-1", "name": "portico-edge-0.3.0", "sizeBytes": 1234, "type": "DISK_IMAGE"}}
	c := newClient(t, f, nil)
	img, err := c.CreateImageFromURL(context.Background(), "portico-edge-0.3.0", "https://example.test/portico-edge-0.3.0.qcow2")
	if err != nil {
		t.Fatalf("CreateImageFromURL: %v", err)
	}
	if img.ExtID != "img-1" || img.SizeBytes != 1234 {
		t.Fatalf("read back = %+v", img)
	}
	b := find(f.requests(), "POST", PathImages).Body
	src := b["source"].(map[string]any)
	if b["name"] != "portico-edge-0.3.0" || b["type"] != "DISK_IMAGE" || src["$objectType"] != "vmm.v4.content.UrlSource" || src["url"] != "https://example.test/portico-edge-0.3.0.qcow2" {
		t.Fatalf("POST body = %v", b)
	}
	found, err := c.FindImage(context.Background(), "portico-edge-0.3.0")
	if err != nil || found.ExtID != "img-1" {
		t.Fatalf("FindImage = %+v, %v", found, err)
	}
	if _, err := c.FindImage(context.Background(), "portico-edge-0.2.0"); err == nil {
		t.Fatal("FindImage of a missing image returned no error")
	}
}

func TestUpdateSubnetGatewayKeepsOtherFields(t *testing.T) {
	f := newFakePC(t)
	s := netSubnet("oen-1", "portico-mi1-ext", "")
	s["isExternal"] = true
	s["ipConfig"].([]any)[0].(map[string]any)["ipv4"].(map[string]any)["defaultGatewayIp"] = map[string]any{"value": "10.100.1.1"}
	for _, k := range []string{"links", "vpc", "ipUsage", "clusterName", "hypervisorType", "virtualSwitch", "migrationState", "bridgeName", "reservedIpAddresses", "dynamicIpAddresses", "tenantId", "$reserved", "$objectType"} {
		s[k] = "set"
	}
	f.objs[PathNetworking+"/subnets/oen-1"] = s
	c := newClient(t, f, nil)
	if err := c.UpdateSubnetGateway(context.Background(), "oen-1", netip.MustParseAddr("10.100.1.5")); err != nil {
		t.Fatalf("UpdateSubnetGateway: %v", err)
	}
	put := find(f.requests(), "PUT", PathNetworking+"/subnets/oen-1")
	if put == nil || put.Header.Get("If-Match") != f.etag {
		t.Fatalf("put = %+v", put)
	}
	ip := put.Body["ipConfig"].([]any)[0].(map[string]any)["ipv4"].(map[string]any)
	if ip["defaultGatewayIp"].(map[string]any)["value"] != "10.100.1.5" || ip["ipSubnet"].(map[string]any)["prefixLength"] != float64(24) ||
		put.Body["name"] != "portico-mi1-ext" || put.Body["vpcReference"] != "vpc-1" || put.Body["isExternal"] != true {
		t.Fatalf("PUT body = %v", put.Body)
	}
	for _, k := range []string{"links", "vpc", "ipUsage", "clusterName", "hypervisorType", "virtualSwitch", "migrationState", "bridgeName", "reservedIpAddresses", "dynamicIpAddresses", "tenantId", "$reserved", "$objectType"} {
		if _, ok := put.Body[k]; ok {
			t.Fatalf("PUT body carries %s", k)
		}
	}
}
