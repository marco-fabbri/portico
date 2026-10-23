package settings

import (
	"sort"
	"strings"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// PickDefaults chooses a site's default cluster and storage container
// (sites usability design 4.1 step 4, write-up 24 item 3):
//   - the cluster is the first AOS cluster by name;
//   - the container is on that cluster, never a system or Nutanix Objects
//     container. A default-container-* comes first, then the most free
//     space, then the name.
//
// It returns "" for what it cannot pick. The MSP can still choose any
// container from the menu.
func PickDefaults(clusters []nutanix.Cluster, containers []nutanix.StorageContainer) (clusterExtID, containerExtID string) {
	var aos []nutanix.Cluster
	for _, c := range clusters {
		if c.IsAOS {
			aos = append(aos, c)
		}
	}
	if len(aos) == 0 {
		return "", ""
	}
	sort.Slice(aos, func(i, j int) bool { return aos[i].Name < aos[j].Name })
	clusterExtID = aos[0].ExtID

	var cand []nutanix.StorageContainer
	for _, sc := range containers {
		if sc.ClusterExtID == clusterExtID && !sc.IsSystem && !sc.IsObjects {
			cand = append(cand, sc)
		}
	}
	sort.Slice(cand, func(i, j int) bool {
		di, dj := strings.HasPrefix(cand[i].Name, "default-container"), strings.HasPrefix(cand[j].Name, "default-container")
		if di != dj {
			return di
		}
		if cand[i].FreeBytes != cand[j].FreeBytes {
			return cand[i].FreeBytes > cand[j].FreeBytes
		}
		return cand[i].Name < cand[j].Name
	})
	if len(cand) > 0 {
		containerExtID = cand[0].ExtID
	}
	return clusterExtID, containerExtID
}
