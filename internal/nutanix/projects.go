package nutanix

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Project is a Projects 2.0 project.
type Project struct {
	ExtID     string `json:"extId"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// ResourceGroup allocates clusters and storage containers to a project.
type ResourceGroup struct {
	ExtID        string `json:"extId"`
	Name         string `json:"name"`
	ProjectExtID string `json:"projectExtId"`
}

// listAll fetches every page of a v4 list into out (a pointer to a slice).
func (c *Client) listAll(ctx context.Context, path string, extra url.Values, out any) error {
	const limit = 100
	var all []map[string]any
	for page := 0; ; page++ {
		q := url.Values{}
		for k, v := range extra {
			q[k] = v
		}
		q.Set("$page", strconv.Itoa(page))
		q.Set("$limit", strconv.Itoa(limit))
		var items []map[string]any
		if _, err := c.do(ctx, http.MethodGet, path, q.Encode(), nil, "", &items); err != nil {
			return err
		}
		all = append(all, items...)
		if len(items) < limit {
			break
		}
	}
	return remarshal(all, out)
}

// ListProjects returns every project, the default one included.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := c.listAll(ctx, PathMultidomain+"/config/projects", nil, &out)
	return out, err
}

// FindProject returns the project with the given name.
func (c *Client) FindProject(ctx context.Context, name string) (*Project, error) {
	ps, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i], nil
		}
	}
	return nil, notFound("project " + name)
}

// CreateProject creates a project (id is the slug the schema requires),
// waits for its task and returns the project as read back.
func (c *Client) CreateProject(ctx context.Context, id, name, description string) (*Project, error) {
	var task taskRef
	body := map[string]string{"id": id, "name": name, "description": description}
	if _, err := c.do(ctx, http.MethodPost, PathMultidomain+"/config/projects", "", body, "", &task); err != nil {
		return nil, err
	}
	if err := c.WaitTask(ctx, task.ExtID); err != nil {
		return nil, err
	}
	return c.FindProject(ctx, name)
}

// DeleteProject deletes a project by extId and waits for the task.
func (c *Client) DeleteProject(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathMultidomain+"/config/projects/"+extID)
}

// deleteWithTask does GET (ETag) → DELETE with If-Match → WaitTask.
func (c *Client) deleteWithTask(ctx context.Context, path string) error {
	etag, err := c.etagOf(ctx, path)
	if err != nil {
		return err
	}
	var task taskRef
	if _, err := c.do(ctx, http.MethodDelete, path, "", nil, etag, &task); err != nil {
		return err
	}
	if task.ExtID == "" {
		return nil
	}
	return c.WaitTask(ctx, task.ExtID)
}

// ListResourceGroups returns the resource groups of a project.
func (c *Client) ListResourceGroups(ctx context.Context, projectExtID string) ([]ResourceGroup, error) {
	var all []ResourceGroup
	if err := c.listAll(ctx, PathMultidomain+"/config/resource-groups", nil, &all); err != nil {
		return nil, err
	}
	var out []ResourceGroup
	for _, rg := range all {
		if rg.ProjectExtID == projectExtID {
			out = append(out, rg)
		}
	}
	return out, nil
}

// CreateResourceGroup allocates one cluster and one storage container to a
// project (Prism Central adds SelfServiceContainer by itself).
func (c *Client) CreateResourceGroup(ctx context.Context, name, projectExtID, clusterExtID, containerExtID string) (*ResourceGroup, error) {
	body := map[string]any{
		"name":         name,
		"projectExtId": projectExtID,
		"placementTargets": []map[string]any{{
			"clusterExtId":      clusterExtID,
			"storageContainers": []map[string]string{{"extId": containerExtID}},
		}},
	}
	var task taskRef
	if _, err := c.do(ctx, http.MethodPost, PathMultidomain+"/config/resource-groups", "", body, "", &task); err != nil {
		return nil, err
	}
	if err := c.WaitTask(ctx, task.ExtID); err != nil {
		return nil, err
	}
	rgs, err := c.ListResourceGroups(ctx, projectExtID)
	if err != nil {
		return nil, err
	}
	for i := range rgs {
		if rgs[i].Name == name {
			return &rgs[i], nil
		}
	}
	return nil, notFound("resource group " + name)
}

// DeleteResourceGroup deletes a resource group and waits for the task.
func (c *Client) DeleteResourceGroup(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathMultidomain+"/config/resource-groups/"+extID)
}
