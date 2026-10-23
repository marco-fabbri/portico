package nutanix

import (
	"context"
	"net/url"
	"regexp"
	"time"
)

// PathClusterMgmt is the clusters API family measured on pc.7.6.0.6.
const PathClusterMgmt = "/api/clustermgmt/v4.0"

// Cluster is a cluster registered to Prism Central.
type Cluster struct {
	ExtID          string
	Name           string
	IsAOS          bool   // false for the Prism Central cluster itself
	IsPrismCentral bool   // the Prism Central's own entry
	Version        string // config.buildInfo.version, e.g. "pc.7.6.0.6"
}

// StorageContainer is a storage container of some cluster.
type StorageContainer struct {
	ExtID        string
	Name         string
	ClusterExtID string
	IsSystem     bool // isInternal, or SelfServiceContainer, NutanixManagementShare, NutanixMetadata
	IsInternal   bool // isInternal: a system container Prism Central marks itself
	// IsObjects: a Nutanix Objects container. No field marks it on pc.7.6,
	// only the name (write-up 24 item 3); it stays choosable, never a default.
	IsObjects bool
	// FreeBytes is filled by the caller from ContainerFreeBytes; 0 is unknown.
	FreeBytes int64
}

var objectsContainerName = regexp.MustCompile(`^objects[a-z0-9]{20,}$`)

// ListClusters returns the clusters with their function.
func (c *Client) ListClusters(ctx context.Context) ([]Cluster, error) {
	var raw []struct {
		ExtID  string `json:"extId"`
		Name   string `json:"name"`
		Config struct {
			ClusterFunction []string `json:"clusterFunction"`
			BuildInfo       struct {
				Version string `json:"version"`
			} `json:"buildInfo"`
		} `json:"config"`
	}
	if err := c.listAll(ctx, PathClusterMgmt+"/config/clusters", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(raw))
	for _, r := range raw {
		cl := Cluster{ExtID: r.ExtID, Name: r.Name, Version: r.Config.BuildInfo.Version}
		for _, f := range r.Config.ClusterFunction {
			switch f {
			case "AOS":
				cl.IsAOS = true
			case "PRISM_CENTRAL":
				cl.IsPrismCentral = true
			}
		}
		out = append(out, cl)
	}
	return out, nil
}

// ListStorageContainers returns every storage container, system ones flagged.
func (c *Client) ListStorageContainers(ctx context.Context) ([]StorageContainer, error) {
	var raw []struct {
		ContainerExtID string `json:"containerExtId"`
		ExtID          string `json:"extId"`
		Name           string `json:"name"`
		ClusterExtID   string `json:"clusterExtId"`
		IsInternal     bool   `json:"isInternal"`
	}
	if err := c.listAll(ctx, PathClusterMgmt+"/config/storage-containers", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]StorageContainer, 0, len(raw))
	for _, r := range raw {
		id := r.ContainerExtID
		if id == "" {
			id = r.ExtID
		}
		sc := StorageContainer{ExtID: id, Name: r.Name, ClusterExtID: r.ClusterExtID, IsInternal: r.IsInternal, IsSystem: r.IsInternal}
		switch r.Name {
		case "SelfServiceContainer", "NutanixManagementShare", "NutanixMetadata":
			sc.IsSystem = true
		}
		sc.IsObjects = !sc.IsSystem && objectsContainerName.MatchString(r.Name)
		out = append(out, sc)
	}
	return out, nil
}

// ContainerFreeBytes returns a storage container's free bytes, the last
// sample of the stats API over the past 15 minutes (measured on pc.7.6.0.6
// as the lab admin: the call needs $startTime and $endTime).
func (c *Client) ContainerFreeBytes(ctx context.Context, extID string) (int64, error) {
	now := time.Now().UTC()
	q := url.Values{}
	q.Set("$startTime", now.Add(-15*time.Minute).Format(time.RFC3339))
	q.Set("$endTime", now.Format(time.RFC3339))
	q.Set("$samplingInterval", "300")
	q.Set("$statType", "LAST")
	var out struct {
		Free []struct {
			Value int64 `json:"value"`
		} `json:"storageFreeBytes"`
	}
	if _, err := c.do(ctx, "GET", PathClusterMgmt+"/stats/storage-containers/"+url.PathEscape(extID), q.Encode(), nil, "", &out); err != nil {
		return 0, err
	}
	if len(out.Free) == 0 {
		return 0, nil
	}
	return out.Free[len(out.Free)-1].Value, nil
}

// PrismCentralVersion returns the build version Prism Central reports
// for itself in the clusters list (its PRISM_CENTRAL entry), "" when the
// entry or the field is absent. Provisional call (design sites §6, D5):
// the service account can list clusters, measured in write-up 07.
func (c *Client) PrismCentralVersion(ctx context.Context) (string, error) {
	cls, err := c.ListClusters(ctx)
	if err != nil {
		return "", err
	}
	return PrismCentralVersionOf(cls), nil
}

// PrismCentralVersionOf picks Prism Central's version from a clusters list.
func PrismCentralVersionOf(cls []Cluster) string {
	for _, cl := range cls {
		if cl.IsPrismCentral {
			return cl.Version
		}
	}
	return ""
}
