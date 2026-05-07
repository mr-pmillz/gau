package commoncrawl_test

import (
	"context"
	"crypto/tls"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/commoncrawl"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestCommonCrawl_Live exercises the real Common Crawl API end-to-end:
// collinfo bootstrap, the pagination probe, and one CDX page. It is gated
// behind GAU_CC_LIVE and is *not* part of the default `go test ./...`
// suite — Common Crawl has historically been hammered by this tool and
// being a polite citizen is the whole point of this fork.
//
// Run explicitly:
//
//	GAU_CC_LIVE=1 go test -run TestCommonCrawl_Live ./pkg/providers/commoncrawl/...
//
// Optional knobs:
//
//	GAU_CC_LIVE_DOMAIN     target domain                (default "example.com")
//	GAU_CC_LIVE_TIMEOUT_S  total wall-clock budget (s)  (default 60)
//
// On success the test cancels its context as soon as the first URL
// arrives, capping live API hits at: collinfo (1) + pagination (1) +
// first page (1) = 3 requests.
func TestCommonCrawl_Live(t *testing.T) {
	if !liveEnabled("GAU_CC_LIVE") {
		t.Skip("GAU_CC_LIVE not set; skipping live Common Crawl integration test")
	}

	domain := envOr("GAU_CC_LIVE_DOMAIN", "example.com")
	totalSec := envOrInt("GAU_CC_LIVE_TIMEOUT_S", 90)

	// Per-request timeout is 1/3 of the total budget so a single slow CC
	// request can't starve the bootstrap → pagination → page sequence.
	perReqSec := totalSec / 3
	if perReqSec < 15 {
		perReqSec = 15
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(totalSec)*time.Second)
	defer cancel()

	cfg := &providers.Config{
		Threads:    1,
		Timeout:    uint(perReqSec),
		MaxRetries: 1, // one retry on transient blip; not enough to abuse CC
		Client: &fasthttp.Client{
			TLSConfig: &tls.Config{InsecureSkipVerify: false}, // verify TLS for real
		},
		// Match the conservative default from .gau.toml (0.2/s = one every
		// 5 seconds). Do NOT raise this.
		RateLimits: providers.RateLimits{CommonCrawl: 0.2},
	}

	c, err := commoncrawl.New(ctx, cfg, providers.Filters{})
	require.NoError(t, err, "live New: collinfo.json bootstrap should succeed")

	results := make(chan string, 64)
	fetchErr := make(chan error, 1)
	go func() {
		fetchErr <- c.Fetch(ctx, domain, results)
		close(results)
	}()

	var got []string
	for u := range results {
		got = append(got, u)
		if len(got) == 1 {
			// First URL proves the plumbing works end-to-end. Cancel now
			// so we don't pull additional pages unnecessarily.
			cancel()
		}
	}
	require.NoError(t, <-fetchErr, "Fetch should return nil after drain (ctx cancel is normalized to nil)")

	require.NotEmpty(t, got,
		"live CC returned zero URLs for %s — either the index is sparse, the URL format string drifted, or the response shape changed",
		domain)

	t.Logf("live Common Crawl returned %d URL(s) for %s; first: %s", len(got), domain, got[0])

	for _, u := range got {
		if _, err := url.Parse(u); err != nil {
			t.Errorf("live CC returned unparseable URL: %q (%v)", u, err)
		}
	}
}

func liveEnabled(envVar string) bool {
	switch os.Getenv(envVar) {
	case "1", "true", "TRUE", "yes":
		return true
	}
	return false
}

func envOr(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}

func envOrInt(envVar string, fallback int) int {
	if v := os.Getenv(envVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
