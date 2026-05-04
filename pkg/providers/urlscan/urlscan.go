// Package urlscan fetches URLs from urlscan.io's search API.
package urlscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	jsoniter "github.com/json-iterator/go"
	"github.com/mr-pmillz/gau/v2/pkg/httpclient"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

const (
	Name           = "urlscan"
	defaultBaseURL = "https://urlscan.io/"
)

var _ providers.Provider = (*Client)(nil)

// Client implements providers.Provider against urlscan.io.
type Client struct {
	config  *providers.Config
	limiter *rate.Limiter
	baseURL string
}

func New(c *providers.Config) *Client {
	base := defaultBaseURL
	if c.URLScan.Host != "" {
		base = ensureTrailingSlash(c.URLScan.Host)
	}
	return &Client{
		config:  c,
		limiter: providers.Limiter(c.RateLimits.URLScan),
		baseURL: base,
	}
}

func ensureTrailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// SetBaseURL overrides the urlscan endpoint. Used by tests.
func (c *Client) SetBaseURL(u string) { c.baseURL = ensureTrailingSlash(u) }

func (c *Client) Name() string { return Name }

func (c *Client) Fetch(ctx context.Context, domain string, results chan string) error {
	var searchAfter string
	var header httpclient.Header
	if c.config.URLScan.APIKey != "" {
		header.Key = "API-Key"
		header.Value = c.config.URLScan.APIKey
	}

	for page := uint(0); ; page++ {
		if err := ctx.Err(); err != nil {
			return nil
		}

		logrus.WithFields(logrus.Fields{"provider": Name, "page": page}).Infof("fetching %s", domain)
		apiURL := c.formatURL(domain, searchAfter)
		resp, err := httpclient.MakeRequest(ctx, c.config.Client, apiURL,
			httpclient.RequestOpts{
				MaxRetries: c.config.MaxRetries,
				Timeout:    c.config.Timeout,
				Limiter:    c.limiter,
			}, header)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			// urlscan rate-limits aggressively. The httpclient surfaces
			// this as ErrRateLimited; treat it as a graceful stop, not a
			// hard error, matching prior behavior so the user gets the
			// URLs we did manage to collect.
			if errors.Is(err, httpclient.ErrRateLimited) {
				logrus.WithField("provider", Name).Warn("urlscan returned 429, stopping")
				return nil
			}
			return fmt.Errorf("fetch urlscan: %w", err)
		}

		var result apiResponse
		decoder := jsoniter.NewDecoder(bytes.NewReader(resp))
		decoder.UseNumber()
		if err = decoder.Decode(&result); err != nil {
			return fmt.Errorf("decode urlscan: %w", err)
		}
		// Some urlscan endpoints embed status inside the body even on 200.
		if result.Status == 429 {
			logrus.WithField("provider", Name).Warn("urlscan body indicated 429, stopping")
			return nil
		}

		total := len(result.Results)
		for i, res := range result.Results {
			if res.Page.Domain == domain ||
				(c.config.IncludeSubdomains && strings.HasSuffix(res.Page.Domain, domain)) {
				select {
				case <-ctx.Done():
					return nil
				case results <- res.Page.URL:
				}
			}
			if i == total-1 {
				sortParam := parseSort(res.Sort)
				if sortParam == "" {
					return nil
				}
				searchAfter = sortParam
			}
		}

		if !result.HasMore {
			return nil
		}
	}
}

func (c *Client) formatURL(domain string, after string) string {
	if after != "" {
		after = "&search_after=" + after
	}
	return fmt.Sprintf(c.baseURL+"api/v1/search/?q=domain:%s&size=100", domain) + after
}
