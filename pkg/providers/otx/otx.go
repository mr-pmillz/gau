// Package otx fetches URLs from AlienVault's Open Threat Exchange.
package otx

import (
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
	Name           = "otx"
	defaultBaseURL = "https://otx.alienvault.com/"
)

var _ providers.Provider = (*Client)(nil)

// Client implements providers.Provider against OTX.
type Client struct {
	config  *providers.Config
	limiter *rate.Limiter
	baseURL string
}

func New(c *providers.Config) *Client {
	base := defaultBaseURL
	if c.OTX != "" {
		base = ensureTrailingSlash(c.OTX)
	}
	return &Client{
		config:  c,
		limiter: providers.Limiter(c.RateLimits.OTX),
		baseURL: base,
	}
}

func ensureTrailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// SetBaseURL overrides the OTX endpoint. Used by tests.
func (c *Client) SetBaseURL(u string) { c.baseURL = ensureTrailingSlash(u) }

func (c *Client) Name() string { return Name }

type otxResult struct {
	HasNext    bool `json:"has_next"`
	ActualSize int  `json:"actual_size"`
	URLList    []struct {
		Domain   string `json:"domain"`
		URL      string `json:"url"`
		Hostname string `json:"hostname"`
		HTTPCode int    `json:"httpcode"`
		PageNum  int    `json:"page_num"`
		FullSize int    `json:"full_size"`
		Paged    bool   `json:"paged"`
	} `json:"url_list"`
}

func (c *Client) Fetch(ctx context.Context, domain string, results chan string) error {
	for page := uint(1); ; page++ {
		if err := ctx.Err(); err != nil {
			return nil
		}

		logrus.WithFields(logrus.Fields{"provider": Name, "page": page - 1}).Infof("fetching %s", domain)
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
			return fmt.Errorf("fetch otx page %d: %w", page, err)
		}

		var result otxResult
		if err := jsoniter.Unmarshal(resp, &result); err != nil {
			return fmt.Errorf("decode otx page %d: %w", page, err)
		}

		for _, entry := range result.URLList {
			select {
			case <-ctx.Done():
				return nil
			case results <- entry.URL:
			}
		}

		if !result.HasNext {
			return nil
		}
	}
}

// formatURL picks `domain` vs `hostname` based on whether the input has a
// subdomain and whether --subs is enabled.
func (c *Client) formatURL(domain string, page uint) string {
	category := "hostname"
	if !providers.HasSubdomain(domain) {
		category = "domain"
	}
	if providers.HasSubdomain(domain) && c.config.IncludeSubdomains {
		domain = providers.Domain(domain)
		category = "domain"
	}
	return fmt.Sprintf("%sapi/v1/indicators/%s/%s/url_list?limit=100&page=%d", c.baseURL, category, domain, page)
}
