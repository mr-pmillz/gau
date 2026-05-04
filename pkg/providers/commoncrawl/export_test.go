package commoncrawl

import (
	"context"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
)

// NewWithCollinfoURL is exported only for tests, via export_test.go. It lets
// a test point the collinfo bootstrap at a httptest.Server.
func NewWithCollinfoURL(ctx context.Context, c *providers.Config, filters providers.Filters, collinfoURL string) (*Client, error) {
	return newWithCollinfoURL(ctx, c, filters, collinfoURL)
}
