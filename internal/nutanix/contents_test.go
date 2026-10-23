package nutanix

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestProjectContentsCountsEveryKind(t *testing.T) {
	f := newFakePC(t)
	for _, k := range contentKinds {
		f.totals[k.path] = 0
	}
	f.totals["/api/vmm/v4.0/ahv/config/vms"] = 2
	f.totals["/api/vmm/v4.0/content/images"] = 1
	delete(f.totals, "/api/microseg/v4.0/config/policies")
	f.fail["GET /api/microseg/v4.0/config/policies"] = 403
	got, err := newClient(t, f, nil).ProjectContents(context.Background(), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["vms"] != 2 || got.Counts["images"] != 1 || len(got.Uncounted) != 1 || got.Uncounted[0] != "security-policies" {
		t.Fatalf("contents = %+v", got)
	}
	if _, ok := got.Counts["security-policies"]; ok {
		t.Fatal("an uncounted kind has a count")
	}
	if got.Empty() || got.String() != "VMs: 2, images: 1" {
		t.Fatalf("Empty %v, String %q", got.Empty(), got.String())
	}
	reqs := f.requests()
	if len(reqs) != len(contentKinds) {
		t.Fatalf("%d requests for %d kinds", len(reqs), len(contentKinds))
	}
	for _, r := range reqs {
		q, _ := url.ParseQuery(r.Query)
		if q.Get("$filter") != "projectExtId eq 'p-1'" || q.Get("$limit") != "1" {
			t.Fatalf("%s query %q", r.Path, r.Query)
		}
	}
	if (Contents{Counts: map[string]int{"vms": 0}}).Empty() != true {
		t.Fatal("zero counts are not empty")
	}
}

func TestProjectContentsFailsOnServerError(t *testing.T) {
	f := newFakePC(t)
	for _, k := range contentKinds {
		f.totals[k.path] = 0
	}
	delete(f.totals, "/api/networking/v4.0/config/floating-ips")
	f.fail["GET /api/networking/v4.0/config/floating-ips"] = 504
	if _, err := newClient(t, f, nil).ProjectContents(context.Background(), "p-1"); err == nil {
		t.Fatal("a 504 counted as empty")
	}
}

func TestWaitTaskFailureIsMarked(t *testing.T) {
	f := newFakePC(t)
	f.status = "FAILED"
	err := newClient(t, f, nil).WaitTask(context.Background(), "t-1")
	if !IsTaskFailed(err) {
		t.Fatalf("task failure not marked: %v", err)
	}
	if IsTaskFailed(&APIError{Status: 500}) {
		t.Fatal("a plain API error marked as a task failure")
	}
}

func TestStorageContainersCarryCluster(t *testing.T) {
	f := newFakePC(t)
	f.lists["/api/clustermgmt/v4.0/config/storage-containers"] = []any{
		map[string]any{"containerExtId": "sc-1", "name": "default-container-1", "clusterExtId": "cl-1"},
	}
	got, err := newClient(t, f, nil).ListStorageContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ExtID != "sc-1" || got[0].ClusterExtID != "cl-1" {
		t.Fatalf("containers = %+v", got)
	}
}

func TestProjectContentsMissingAPIIsUncounted(t *testing.T) {
	f := newFakePC(t)
	for _, k := range contentKinds {
		f.totals[k.path] = 0
	}
	delete(f.totals, "/api/dataprotection/v4.0/config/recovery-points")
	f.fail["GET /api/dataprotection/v4.0/config/recovery-points"] = 404
	delete(f.totals, "/api/networking/v4.0/config/vpcs")
	f.fail["GET /api/networking/v4.0/config/vpcs"] = 501
	got, err := newClient(t, f, nil).ProjectContents(context.Background(), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Uncounted) != 2 || !got.Empty() {
		t.Fatalf("contents = %+v", got)
	}
}

func TestListStorageContainersFlags(t *testing.T) {
	f := newFakePC(t)
	f.lists["/api/clustermgmt/v4.0/config/storage-containers"] = []any{
		map[string]any{"containerExtId": "sc-int", "name": "NutanixMetadataContainer", "clusterExtId": "cl-1", "isInternal": true},
		map[string]any{"containerExtId": "sc-obj", "name": "objectsl889411dccd9a44125aa3e31c2596dcf9", "clusterExtId": "cl-1"},
		map[string]any{"containerExtId": "sc-ss", "name": "SelfServiceContainer", "clusterExtId": "cl-1"},
		map[string]any{"containerExtId": "sc-def", "name": "default-container-1", "clusterExtId": "cl-1"},
	}
	got, err := newClient(t, f, nil).ListStorageContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]StorageContainer{}
	for _, c := range got {
		by[c.ExtID] = c
	}
	if c := by["sc-int"]; !c.IsInternal || !c.IsSystem || c.IsObjects {
		t.Errorf("internal container = %+v", c)
	}
	if c := by["sc-obj"]; !c.IsObjects || c.IsSystem || c.IsInternal {
		t.Errorf("objects container = %+v", c)
	}
	if c := by["sc-ss"]; !c.IsSystem || c.IsInternal {
		t.Errorf("SelfServiceContainer = %+v", c)
	}
	if c := by["sc-def"]; c.IsSystem || c.IsObjects || c.IsInternal {
		t.Errorf("default container = %+v", c)
	}
}

func TestContainerFreeBytesReadsLastSample(t *testing.T) {
	f := newFakePC(t)
	f.objs["/api/clustermgmt/v4.0/stats/storage-containers/sc-1"] = map[string]any{
		"storageFreeBytes": []any{
			map[string]any{"value": 100, "timestamp": "2026-10-23T13:20:00Z"},
			map[string]any{"value": 621750432032, "timestamp": "2026-10-23T13:25:00Z"},
		},
	}
	got, err := newClient(t, f, nil).ContainerFreeBytes(context.Background(), "sc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 621750432032 {
		t.Fatalf("free = %d", got)
	}
	q := f.requests()[0].Query
	for _, k := range []string{"%24startTime=", "%24endTime=", "%24statType=LAST"} {
		if !strings.Contains(q, k) {
			t.Errorf("query %q lacks %s", q, k)
		}
	}
}
