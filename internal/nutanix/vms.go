package nutanix

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
)

const (
	PathVMs        = "/api/vmm/v4.0/ahv/config/vms"
	PathCategories = "/api/prism/v4.0/config/categories"
)

// VM is an AHV VM as Portico needs it: where it runs, whether it is on,
// its categories as key:value.
type VM struct {
	ExtID, Name, ClusterExtID string
	PowerOn                   bool
	Categories                []string
}

// VMSpec is what CreateVM builds: one disk from an image, one NIC on a
// subnet, cloud-init user data on a config drive (the edge router,
// design 4.2), one category when CategoryExtID is set.
type VMSpec struct {
	Name, ClusterExtID, ImageExtID, SubnetExtID string
	VCPUs                                       int
	MemoryBytes, DiskBytes                      int64
	UserData                                    string
	CategoryExtID                               string
}

// Category is a Prism Central category value.
type Category struct {
	ExtID string `json:"extId"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type rawVM struct {
	ExtID   string `json:"extId"`
	Name    string `json:"name"`
	Cluster struct {
		ExtID string `json:"extId"`
	} `json:"cluster"`
	PowerState string `json:"powerState"`
	Categories []struct {
		ExtID string `json:"extId"`
	} `json:"categories"`
}

func (r rawVM) vm(names map[string]string) VM {
	v := VM{ExtID: r.ExtID, Name: r.Name, ClusterExtID: r.Cluster.ExtID, PowerOn: r.PowerState == "ON"}
	for _, c := range r.Categories {
		if n, ok := names[c.ExtID]; ok {
			v.Categories = append(v.Categories, n)
		}
	}
	return v
}

// ListCategories returns every category value.
func (c *Client) ListCategories(ctx context.Context) ([]Category, error) {
	var out []Category
	err := c.listAll(ctx, PathCategories, nil, &out)
	return out, err
}

// categoryNames maps category extIds to key:value; nil, nil when none of
// the VMs carries a category, so a listing without any makes one call.
func (c *Client) categoryNames(ctx context.Context, vms ...rawVM) (map[string]string, error) {
	found := false
	for _, v := range vms {
		found = found || len(v.Categories) > 0
	}
	if !found {
		return nil, nil
	}
	cats, err := c.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(cats))
	for _, cat := range cats {
		names[cat.ExtID] = cat.Key + ":" + cat.Value
	}
	return names, nil
}

// EnsureCategory returns the extId of the category key:value, creating
// it when missing (the create is synchronous).
func (c *Client) EnsureCategory(ctx context.Context, key, value string) (string, error) {
	var found []Category
	q := url.Values{"$filter": {"key eq '" + key + "' and value eq '" + value + "'"}}
	if err := c.listAll(ctx, PathCategories, q, &found); err != nil {
		return "", err
	}
	for _, cat := range found {
		if cat.Key == key && cat.Value == value {
			return cat.ExtID, nil
		}
	}
	var created Category
	if _, err := c.do(ctx, http.MethodPost, PathCategories, "", map[string]any{"key": key, "value": value, "description": "Managed by Portico"}, "", &created); err != nil {
		return "", err
	}
	return created.ExtID, nil
}

// ListVMs returns every VM with its categories resolved to key:value.
func (c *Client) ListVMs(ctx context.Context) ([]VM, error) {
	var raw []rawVM
	if err := c.listAll(ctx, PathVMs, nil, &raw); err != nil {
		return nil, err
	}
	names, err := c.categoryNames(ctx, raw...)
	if err != nil {
		return nil, err
	}
	out := make([]VM, len(raw))
	for i := range raw {
		out[i] = raw[i].vm(names)
	}
	return out, nil
}

// GetVM reads one VM.
func (c *Client) GetVM(ctx context.Context, extID string) (*VM, error) {
	var raw rawVM
	if _, err := c.do(ctx, http.MethodGet, PathVMs+"/"+extID, "", nil, "", &raw); err != nil {
		return nil, err
	}
	names, err := c.categoryNames(ctx, raw)
	if err != nil {
		return nil, err
	}
	v := raw.vm(names)
	return &v, nil
}

// CreateVM creates the VM of s, powered off: legacy BIOS boot, one SCSI
// disk cloned from the image, one virtio NIC, cloud-init on a config
// drive (CONFIG_DRIVE_V2) as lab/network/edge-router.sh measured.
func (c *Client) CreateVM(ctx context.Context, s VMSpec) (*VM, error) {
	body := map[string]any{
		"name": s.Name, "description": "Managed by Portico", "numSockets": 1, "numCoresPerSocket": s.VCPUs, "memorySizeBytes": s.MemoryBytes,
		"cluster":    map[string]any{"extId": s.ClusterExtID},
		"bootConfig": map[string]any{"$objectType": "vmm.v4.ahv.config.LegacyBoot"},
		"disks": []map[string]any{{
			"diskAddress": map[string]any{"busType": "SCSI", "index": 0},
			"backingInfo": map[string]any{"$objectType": "vmm.v4.ahv.config.VmDisk", "diskSizeBytes": s.DiskBytes,
				"dataSource": map[string]any{"reference": map[string]any{"$objectType": "vmm.v4.ahv.config.ImageReference", "imageExtId": s.ImageExtID}}},
		}},
		"nics": []map[string]any{{
			"backingInfo": map[string]any{"$objectType": "vmm.v4.ahv.config.EmulatedNic", "model": "VIRTIO"},
			"networkInfo": map[string]any{"nicType": "NORMAL_NIC", "subnet": map[string]any{"extId": s.SubnetExtID}},
		}},
	}
	if s.UserData != "" {
		body["guestCustomization"] = map[string]any{"config": map[string]any{
			"$objectType": "vmm.v4.ahv.config.CloudInit", "datasourceType": "CONFIG_DRIVE_V2",
			"cloudInitScript": map[string]any{"$objectType": "vmm.v4.ahv.config.Userdata", "value": base64.StdEncoding.EncodeToString([]byte(s.UserData))},
		}}
	}
	if s.CategoryExtID != "" {
		body["categories"] = []map[string]any{{"extId": s.CategoryExtID}}
	}
	return createWithTask(ctx, c, PathVMs, s.Name, body, c.ListVMs, func(all []VM) *VM {
		for i := range all {
			if all[i].Name == s.Name && all[i].ClusterExtID == s.ClusterExtID {
				return &all[i]
			}
		}
		return nil
	})
}

// PowerVM turns a VM on or off. Off is the hard power-off: the edge
// router has no guest agent, and a router with no state to flush loses
// nothing.
func (c *Client) PowerVM(ctx context.Context, extID string, on bool) error {
	action := "power-off"
	if on {
		action = "power-on"
	}
	path := PathVMs + "/" + extID
	etag, err := c.etagOf(ctx, path)
	if err != nil {
		return err
	}
	var task taskRef
	if _, err := c.do(ctx, http.MethodPost, path+"/$actions/"+action, "", nil, etag, &task); err != nil {
		return err
	}
	if task.ExtID == "" {
		return nil
	}
	return c.WaitTask(ctx, task.ExtID)
}

// DeleteVM deletes a VM; Prism Central refuses a running one, so the
// caller powers it off first.
func (c *Client) DeleteVM(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathVMs+"/"+extID)
}
