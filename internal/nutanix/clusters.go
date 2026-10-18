package nutanix

import "context"

// PathClusterMgmt is the clusters API family measured on pc.7.6.0.6.
const PathClusterMgmt = "/api/clustermgmt/v4.0"

// Cluster is a cluster registered to Prism Central.
type Cluster struct {
	ExtID string
	Name  string
	IsAOS bool // false for the Prism Central cluster itself
}

// StorageContainer is a storage container of some cluster.
type StorageContainer struct {
	ExtID    string
	Name     string
	IsSystem bool // SelfServiceContainer, NutanixManagementShare, NutanixMetadata
}

// ListClusters returns the clusters with their function.
func (c *Client) ListClusters(ctx context.Context) ([]Cluster, error) {
	var raw []struct {
		ExtID  string `json:"extId"`
		Name   string `json:"name"`
		Config struct {
			ClusterFunction []string `json:"clusterFunction"`
		} `json:"config"`
	}
	if err := c.listAll(ctx, PathClusterMgmt+"/config/clusters", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(raw))
	for _, r := range raw {
		cl := Cluster{ExtID: r.ExtID, Name: r.Name}
		for _, f := range r.Config.ClusterFunction {
			if f == "AOS" {
				cl.IsAOS = true
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
		sc := StorageContainer{ExtID: id, Name: r.Name}
		switch r.Name {
		case "SelfServiceContainer", "NutanixManagementShare", "NutanixMetadata":
			sc.IsSystem = true
		}
		out = append(out, sc)
	}
	return out, nil
}
