package nutanix

import (
	"context"
	"net/url"
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
