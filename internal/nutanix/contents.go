package nutanix

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// contentKinds are what a Projects 2.0 project can hold, each listed by
// project with $filter=projectExtId (write-up 24, item 1). A subnet or a
// category shared into the project carries its owner's id, not the
// project's, so the filter counts what was made in the project only.
var contentKinds = []struct{ key, path, name string }{
	{"vms", "/api/vmm/v4.0/ahv/config/vms", "VMs"},
	{"images", "/api/vmm/v4.0/content/images", "images"},
	{"templates", "/api/vmm/v4.0/content/templates", "templates"},
	{"ovas", "/api/vmm/v4.0/content/ovas", "OVAs"},
	{"volume-groups", "/api/volumes/v4.0/config/volume-groups", "volume groups"},
	{"subnets", "/api/networking/v4.0/config/subnets", "subnets"},
	{"vpcs", "/api/networking/v4.0/config/vpcs", "VPCs"},
	{"floating-ips", "/api/networking/v4.0/config/floating-ips", "floating IPs"},
	{"security-policies", "/api/microseg/v4.0/config/policies", "security policies"},
	{"categories", "/api/prism/v4.0/config/categories", "categories"},
	{"recovery-points", "/api/dataprotection/v4.0/config/recovery-points", "recovery points"},
}

// Contents is what a project holds, per kind. Uncounted are the kinds
// Prism Central would not list for this account (403 or 400): not counted
// is not empty, and Prism Central's own refusal to delete a project that
// still holds something is the safety net (write-up 24).
type Contents struct {
	Counts    map[string]int
	Uncounted []string
}

// Empty reports whether no counted kind holds anything.
func (c Contents) Empty() bool {
	for _, n := range c.Counts {
		if n > 0 {
			return false
		}
	}
	return true
}

// String names what is there, "VMs: 2, images: 1".
func (c Contents) String() string {
	var parts []string
	for _, k := range contentKinds {
		if n := c.Counts[k.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", k.name, n))
		}
	}
	return strings.Join(parts, ", ")
}

// ProjectContents counts what the project holds, every kind in parallel.
// A kind answering 403 or 400 is recorded as uncounted; any other error
// (a 5xx, a timeout) fails the whole count.
func (c *Client) ProjectContents(ctx context.Context, projectExtID string) (Contents, error) {
	q := url.Values{"$limit": {"1"}, "$filter": {"projectExtId eq '" + projectExtID + "'"}}.Encode()
	type result struct {
		n   int
		err error
	}
	results := make([]result, len(contentKinds))
	var wg sync.WaitGroup
	for i, k := range contentKinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var total int
			_, err := c.call(ctx, http.MethodGet, k.path, q, nil, "", nil, &total)
			results[i] = result{total, err}
		}()
	}
	wg.Wait()
	out := Contents{Counts: map[string]int{}}
	for i, k := range contentKinds {
		var apiErr *APIError
		switch err := results[i].err; {
		case err == nil:
			out.Counts[k.key] = results[i].n
		case errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusBadRequest):
			out.Uncounted = append(out.Uncounted, k.key)
		default:
			return Contents{}, fmt.Errorf("counting %s: %w", k.name, err)
		}
	}
	return out, nil
}
