// Package commoncrawl fetches URLs from the Common Crawl CDX index.
package commoncrawl

import (
	"bufio"
	"bytes"
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
	Name              = "commoncrawl"
	defaultCollinfoURL = "http://index.commoncrawl.org/collinfo.json"
)

// verify interface compliance
var _ providers.Provider = (*Client)(nil)

// Client implements providers.Provider against the Common Crawl CDX index.
type Client struct {
	filters providers.Filters
	config  *providers.Config
	limiter *rate.Limiter

	// apiURL is the resolved CDX-API endpoint for the latest crawl, fetched
	// once during New from the collinfo.json index.
	apiURL string
}

// New fetches the list of available CommonCrawl indexes and selects the
// most recent one. The ctx governs that single bootstrap request.
func New(ctx context.Context, c *providers.Config, filters providers.Filters) (*Client, error) {
	return newWithCollinfoURL(ctx, c, filters, defaultCollinfoURL)
}

// newWithCollinfoURL is the test seam — bootstrap against an arbitrary URL.
func newWithCollinfoURL(ctx context.Context, c *providers.Config, filters providers.Filters, collinfoURL string) (*Client, error) {
	limiter := providers.Limiter(c.RateLimits.CommonCrawl)
	resp, err := httpclient.MakeRequest(ctx, c.Client, collinfoURL,
		httpclient.RequestOpts{
			MaxRetries: c.MaxRetries,
			Timeout:    c.Timeout,
			Limiter:    limiter,
		})
	if err != nil {
		return nil, fmt.Errorf("fetch collinfo.json: %w", err)
	}

	var r apiResult
	if err = jsoniter.Unmarshal(resp, &r); err != nil {
		return nil, fmt.Errorf("decode collinfo.json: %w", err)
	}
	if len(r) == 0 {
		return nil, errors.New("commoncrawl: collinfo.json returned no indexes")
	}

	return &Client{
		config:  c,
		filters: filters,
		limiter: limiter,
		apiURL:  r[0].API,
	}, nil
}

func (c *Client) Name() string { return Name }

// Fetch fetches all urls for a given domain and sends them to a channel.
// It returns an error should one occur (other than ctx cancellation, which
// returns nil so the runner can drain cleanly).
func (c *Client) Fetch(ctx context.Context, domain string, results chan string) error {
	p, err := c.getPagination(ctx, domain)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	if p.Pages == 0 {
		logrus.WithFields(logrus.Fields{"provider": Name}).Infof("no results for %s", domain)
		return nil
	}

	for page := uint(0); page < p.Pages; page++ {
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
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("fetch commoncrawl page %d: %w", page, err)
		}

		sc := bufio.NewScanner(bytes.NewReader(resp))
		for sc.Scan() {
			var res apiResponse
			if err := jsoniter.Unmarshal(sc.Bytes(), &res); err != nil {
				return fmt.Errorf("decode commoncrawl page %d: %w", page, err)
			}
			if res.Error != "" {
				return fmt.Errorf("commoncrawl: %s", res.Error)
			}

			select {
			case <-ctx.Done():
				return nil
			case results <- res.URL:
			}
		}
	}
	return nil
}

func (c *Client) formatURL(domain string, page uint) string {
	if c.config.IncludeSubdomains {
		domain = "*." + domain
	}
	filterParams := c.filters.GetParameters(false)
	return fmt.Sprintf("%s?url=%s/*&output=json&fl=url&page=%d", c.apiURL, domain, page) + filterParams
}

// getPagination asks the CDX index how many pages the query has.
func (c *Client) getPagination(ctx context.Context, domain string) (paginationResult, error) {
	var r paginationResult
	url := c.formatURL(domain, 0) + "&showNumPages=true"
	resp, err := httpclient.MakeRequest(ctx, c.config.Client, url,
		httpclient.RequestOpts{
			MaxRetries: c.config.MaxRetries,
			Timeout:    c.config.Timeout,
			Limiter:    c.limiter,
		})
	if err != nil {
		return r, err
	}
	if err = jsoniter.Unmarshal(resp, &r); err != nil {
		return r, fmt.Errorf("decode pagination: %w", err)
	}
	return r, nil
}
