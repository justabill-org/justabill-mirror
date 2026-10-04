package govinfo

import (
	"context"
	"net/url"
)

// FetchMODS downloads a package's MODS metadata (GET /packages/{packageId}/mods), which for a
// bill lists the US Code sections it cites.
func (c *Client) FetchMODS(ctx context.Context, packageID string) ([]byte, error) {
	return c.doGet(ctx, c.buildURL("/packages/"+url.PathEscape(packageID)+"/mods"))
}
