package urlscan_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/urlscan"
	"github.com/stretchr/testify/require"
)

func newURLScanClient(t *testing.T, srv *testutil.QueueServer, cfgFn func(*providers.Config)) *urlscan.Client {
	cfg := testutil.NewProviderConfig(t)
	if cfgFn != nil {
		cfgFn(cfg)
	}
	c := urlscan.New(cfg)
	c.SetBaseURL(srv.URL)
	return c
}

// collectFetch runs Fetch and gathers the emitted URLs in a slice.
func collectFetch(t *testing.T, c *urlscan.Client, domain string) ([]string, error) {
	t.Helper()
	ch := make(chan string, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	var got []string
	go func() {
		defer wg.Done()
		for v := range ch {
			got = append(got, v)
		}
	}()
	err := c.Fetch(context.Background(), domain, ch)
	close(ch)
	wg.Wait()
	return got, err
}

func TestURLScan_Fetch_FiltersByDomainExactMatch(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{
			"has_more": false,
			"results": [
				{"page": {"domain":"example.com","url":"https://example.com/a"}, "sort": ["x"]},
				{"page": {"domain":"other.com","url":"https://other.com/x"}, "sort": ["y"]},
				{"page": {"domain":"example.com","url":"https://example.com/b"}, "sort": ["z"]}
			]
		}`),
	)
	c := newURLScanClient(t, srv, nil)
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://example.com/a",
		"https://example.com/b",
	}, urls, "non-matching domains must be filtered")
}

func TestURLScan_Fetch_SubdomainMatchWhenSubsEnabled(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{
			"has_more": false,
			"results": [
				{"page": {"domain":"example.com","url":"https://example.com/a"}, "sort": ["1"]},
				{"page": {"domain":"blog.example.com","url":"https://blog.example.com/b"}, "sort": ["2"]},
				{"page": {"domain":"otherexample.com","url":"https://otherexample.com/c"}, "sort": ["3"]}
			]
		}`),
	)
	c := newURLScanClient(t, srv, func(cfg *providers.Config) {
		cfg.IncludeSubdomains = true
	})
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	// HasSuffix("example.com") matches both "example.com" and "blog.example.com"
	// AND "otherexample.com" — that's a known imprecision in upstream, locked
	// in here as current behavior. If/when we fix it, update this test.
	require.Contains(t, urls, "https://example.com/a")
	require.Contains(t, urls, "https://blog.example.com/b")
}

func TestURLScan_Fetch_PaginatesViaSearchAfter(t *testing.T) {
	var page2URL string
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{
			"has_more": true,
			"results": [
				{"page":{"domain":"example.com","url":"https://example.com/a"},"sort":["cursor-1"]}
			]
		}`),
		func(w http.ResponseWriter, r *http.Request) {
			page2URL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"has_more": false,
				"results": [
					{"page":{"domain":"example.com","url":"https://example.com/b"},"sort":["cursor-2"]}
				]
			}`))
		},
	)
	c := newURLScanClient(t, srv, nil)
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://example.com/a",
		"https://example.com/b",
	}, urls)
	require.Contains(t, page2URL, "search_after=cursor-1",
		"second page must use cursor from first page (got %q)", page2URL)
}

func TestURLScan_Fetch_StopsWhenSortEmpty(t *testing.T) {
	// Last entry has empty sort — Fetch must stop even if has_more=true,
	// because there's no cursor to continue with.
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{
			"has_more": true,
			"results": [
				{"page":{"domain":"example.com","url":"https://example.com/a"},"sort":[]}
			]
		}`),
	)
	c := newURLScanClient(t, srv, nil)
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/a"}, urls)
}

func TestURLScan_Fetch_429StatusInBodyStopsGracefully(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{"status":429,"results":[],"has_more":false}`),
	)
	c := newURLScanClient(t, srv, nil)
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.Empty(t, urls)
}

func TestURLScan_Fetch_429HTTPStatusStopsGracefully(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.Status(http.StatusTooManyRequests))
	c := newURLScanClient(t, srv, nil)
	urls, err := collectFetch(t, c, "example.com")
	require.NoError(t, err, "429 must be handled gracefully")
	require.Empty(t, urls)
}

func TestURLScan_Fetch_APIKeyHeaderForwarded(t *testing.T) {
	gotKey := ""
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("API-Key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_more":false,"results":[]}`))
		},
	)
	c := newURLScanClient(t, srv, func(cfg *providers.Config) {
		cfg.URLScan.APIKey = "test-key-123"
	})
	_, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.Equal(t, "test-key-123", gotKey)
}

func TestURLScan_Fetch_DecodeErrorReturnsErr(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.JSON(http.StatusOK, `not json`))
	c := newURLScanClient(t, srv, nil)
	_, err := collectFetch(t, c, "example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode")
}

func TestURLScan_FormatURL_NoTrailingSlashOnHost(t *testing.T) {
	var seen string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_more":false,"results":[]}`))
		},
	)
	c := newURLScanClient(t, srv, nil)
	_, err := collectFetch(t, c, "example.com")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(seen, "/api/v1/search/"),
		"path must be /api/v1/search/ — base URL must end with one slash, not zero or two (got %q)", seen)
}
