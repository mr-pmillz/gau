// Package wayback fetches URLs from the Wayback Machine's CDX API.
package wayback

import (
	"context"
	"errors"
	"fmt"

	jsoniter "github.com/json-iterator/go"
	"github.com/mr-pmillz/gau/v2/pkg/httpclient"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

const (
	Name           = "wayback"
	defaultBaseURL = "https://web.archive.org/cdx/search/cdx"
)

// verify interface compliance
var _ providers.Provider = (*Client)(nil)

// Client implements providers.Provider against the Wayback CDX API.
type Client struct {
	filters providers.Filters
	config  *providers.Config
	limiter *rate.Limiter
	baseURL string
}

func New(config *providers.Config, filters providers.Filters) *Client {
	return &Client{
		filters: filters,
		config:  config,
		limiter: providers.Limiter(config.RateLimits.Wayback),
		baseURL: defaultBaseURL,
	}
}

// SetBaseURL overrides the default Wayback CDX endpoint. Used by tests.
func (c *Client) SetBaseURL(u string) { c.baseURL = u }

func (c *Client) Name() string { return Name }

// waybackResult holds the response from the wayback CDX API. It's a
// 2-D array where row 0 is column headers and rows 1..n are matches.
type waybackResult [][]string

// Fetch fetches all urls for a given domain and sends them to a channel.
//
// The pagination loop terminates on:
//   - empty page (CDX has no more rows for this query),
//   - 400 from the API (CDX returns 400 when the page index is past the end),
//   - context cancellation.
func (c *Client) Fetch(ctx context.Context, domain string, results chan string) error {
	for page := uint(0); ; page++ {
		if err := ctx.Err(); err != nil {
			return nil
		}

		logrus.WithFields(logrus.Fields{"provider": Name, "page": page}).Infof("fetching %s", domain)

		apiURL := c.formatURL(domain, page)
		resp, err := httpclient.MakeRequest(ctx, c.config.Client, apiURL,
			httpclient.RequestOpts{
				MaxRetries: c.config.MaxRetries,
				Timeout:    c.config.Timeout,
				Limiter:    c.limiter,
			})
		if err != nil {
			if errors.Is(err, httpclient.ErrBadRequest) {
				return nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("failed to fetch wayback page %d: %w", page, err)
		}

		var result waybackResult
		if err = jsoniter.Unmarshal(resp, &result); err != nil {
			return fmt.Errorf("failed to decode wayback page %d: %w", page, err)
		}

		// CDX's pagination is unreliable when filters are active — the only
		// reliable signal that we've reached the end is an empty (or
		// header-only) result.
		if len(result) <= 1 {
			return nil
		}

		// Row 0 is the column header; skip it.
		for _, entry := range result[1:] {
			if len(entry) == 0 {
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			case results <- entry[0]:
			}
		}
	}
}

// formatURL returns a fully-qualified Wayback CDX URL for the given page.
func (c *Client) formatURL(domain string, page uint) string {
	if c.config.IncludeSubdomains {
		domain = "*." + domain
	}
	filterParams := c.filters.GetParameters(true)
	return fmt.Sprintf(
		"%s?url=%s/*&output=json&collapse=urlkey&fl=original&pageSize=100&page=%d",
		c.baseURL, domain, page,
	) + filterParams
}
