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

var errURLScanStop = errors.New("stop urlscan fetch")

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
	header := c.authHeader()
	searchAfter := ""

	for page := uint(0); ; page++ {
		if err := ctx.Err(); err != nil {
			return nil
		}

		result, err := c.fetchPage(ctx, domain, searchAfter, page, header)
		if errors.Is(err, errURLScanStop) {
			return nil
		}
		if err != nil {
			return err
		}

		if err := c.emitMatchingURLs(ctx, domain, result.Results, results); err != nil {
			return nil
		}

		if !result.HasMore {
			return nil
		}

		searchAfter = nextSearchAfter(result.Results)
		if searchAfter == "" {
			return nil
		}
	}
}

func (c *Client) authHeader() httpclient.Header {
	if c.config.URLScan.APIKey == "" {
		return httpclient.Header{}
	}
	return httpclient.Header{Key: "API-Key", Value: c.config.URLScan.APIKey}
}

func (c *Client) fetchPage(
	ctx context.Context,
	domain string,
	searchAfter string,
	page uint,
	header httpclient.Header,
) (apiResponse, error) {
	logrus.WithFields(logrus.Fields{"provider": Name, "page": page}).Infof("fetching %s", domain)
	resp, err := httpclient.MakeRequest(ctx, c.config.Client, c.formatURL(domain, searchAfter),
		httpclient.RequestOpts{
			MaxRetries: c.config.MaxRetries,
			Timeout:    c.config.Timeout,
			Limiter:    c.limiter,
		}, header)
	if err != nil {
		return apiResponse{}, handleRequestError(err)
	}

	result, err := decodeResponse(resp)
	if err != nil {
		return apiResponse{}, err
	}
	if result.Status == 429 {
		logrus.WithField("provider", Name).Warn("urlscan body indicated 429, stopping")
		return apiResponse{}, errURLScanStop
	}
	return result, nil
}

func handleRequestError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errURLScanStop
	}
	// urlscan rate-limits aggressively. Treat 429 as a graceful stop so the
	// user still gets URLs already collected.
	if errors.Is(err, httpclient.ErrRateLimited) {
		logrus.WithField("provider", Name).Warn("urlscan returned 429, stopping")
		return errURLScanStop
	}
	return fmt.Errorf("fetch urlscan: %w", err)
}

func decodeResponse(resp []byte) (apiResponse, error) {
	var result apiResponse
	decoder := jsoniter.NewDecoder(bytes.NewReader(resp))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return apiResponse{}, fmt.Errorf("decode urlscan: %w", err)
	}
	return result, nil
}

func (c *Client) emitMatchingURLs(
	ctx context.Context,
	domain string,
	searchResults []searchResult,
	results chan string,
) error {
	for _, res := range searchResults {
		if !c.matchesDomain(res.Page.Domain, domain) {
			continue
		}
		if err := emitResult(ctx, results, res.Page.URL); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) matchesDomain(candidate string, domain string) bool {
	return candidate == domain || (c.config.IncludeSubdomains && strings.HasSuffix(candidate, domain))
}

func emitResult(ctx context.Context, results chan string, url string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case results <- url:
		return nil
	}
}

func nextSearchAfter(results []searchResult) string {
	if len(results) == 0 {
		return ""
	}
	return parseSort(results[len(results)-1].Sort)
}

func (c *Client) formatURL(domain string, after string) string {
	if after != "" {
		after = "&search_after=" + after
	}
	return fmt.Sprintf(c.baseURL+"api/v1/search/?q=domain:%s&size=100", domain) + after
}
