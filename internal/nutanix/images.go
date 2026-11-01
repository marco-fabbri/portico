package nutanix

import "context"

const PathImages = "/api/vmm/v4.0/content/images"

// Image is a disk image in Prism Central's image service.
type Image struct {
	ExtID     string `json:"extId"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ListImages returns every image.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var out []Image
	err := c.listAll(ctx, PathImages, nil, &out)
	return out, err
}

// FindImage returns the image of that name.
func (c *Client) FindImage(ctx context.Context, name string) (*Image, error) {
	all, err := c.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, notFound(name)
}

// CreateImageFromURL registers a disk image Prism Central downloads
// from url (write-up 38: 184 s for the edge image); the task ends when
// the download does.
func (c *Client) CreateImageFromURL(ctx context.Context, name, url string) (*Image, error) {
	body := map[string]any{"name": name, "type": "DISK_IMAGE", "description": "Managed by Portico",
		"source": map[string]any{"$objectType": "vmm.v4.content.UrlSource", "url": url}}
	return createWithTask(ctx, c, PathImages, name, body, c.ListImages, func(all []Image) *Image {
		for i := range all {
			if all[i].Name == name {
				return &all[i]
			}
		}
		return nil
	})
}

// DeleteImage deletes an image.
func (c *Client) DeleteImage(ctx context.Context, extID string) error {
	return c.deleteWithTask(ctx, PathImages+"/"+extID)
}
