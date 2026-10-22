package nutanix

import "context"

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
	IsSystem     bool // SelfServiceContainer, NutanixManagementShare, NutanixMetadata
}

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
		sc := StorageContainer{ExtID: id, Name: r.Name, ClusterExtID: r.ClusterExtID}
		switch r.Name {
		case "SelfServiceContainer", "NutanixManagementShare", "NutanixMetadata":
			sc.IsSystem = true
		}
		out = append(out, sc)
	}
	return out, nil
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
