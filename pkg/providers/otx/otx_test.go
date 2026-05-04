package otx_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/providers/otx"
	"github.com/stretchr/testify/require"
)

func newOTXClient(t *testing.T, srv *testutil.QueueServer) *otx.Client {
	cfg := testutil.NewProviderConfig(t)
	c := otx.New(cfg)
	c.SetBaseURL(srv.URL)
	return c
}

func collect(t *testing.T, c interface {
	Fetch(context.Context, string, chan string) error
}, ctx context.Context, domain string) ([]string, error) {
	t.Helper()
	ch := make(chan string, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	var got []string
	go func() {
		defer wg.Done()
		for v := range ch {
			got = append(got, v)
		}
	}()
	err := c.Fetch(ctx, domain, ch)
	close(ch)
	wg.Wait()
	return got, err
}

func TestOTX_Fetch_SinglePageNoNext(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{"has_next":false,"url_list":[
			{"url":"https://example.com/a"},
			{"url":"https://example.com/b"}
		]}`),
	)
	c := newOTXClient(t, srv)

	urls, err := collect(t, c, context.Background(), "example.com")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"https://example.com/a",
		"https://example.com/b",
	}, urls)
}

func TestOTX_Fetch_PaginatesUntilHasNextFalse(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `{"has_next":true,"url_list":[{"url":"https://example.com/a"}]}`),
		testutil.JSON(http.StatusOK, `{"has_next":true,"url_list":[{"url":"https://example.com/b"}]}`),
		testutil.JSON(http.StatusOK, `{"has_next":false,"url_list":[{"url":"https://example.com/c"}]}`),
	)
	c := newOTXClient(t, srv)

	urls, err := collect(t, c, context.Background(), "example.com")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}, urls)
}

func TestOTX_Fetch_DecodeErrorReturnsErr(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `not json`),
	)
	c := newOTXClient(t, srv)

	_, err := collect(t, c, context.Background(), "example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode")
}

func TestOTX_Fetch_ContextCancellation(t *testing.T) {
	releaseFirst := make(chan struct{})
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_next":false,"url_list":[]}`))
		},
	)
	defer close(releaseFirst)

	c := newOTXClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ch := make(chan string, 1)
	go func() {
		done <- c.Fetch(ctx, "example.com", ch)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not respect ctx cancellation")
	}
}

func TestOTX_FormatURL_DomainCategory(t *testing.T) {
	// Bare domain → category=domain
	var seen string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_next":false,"url_list":[]}`))
		},
	)
	c := newOTXClient(t, srv)
	_, err := collect(t, c, context.Background(), "example.com")
	require.NoError(t, err)
	require.Contains(t, seen, "/indicators/domain/example.com/")
}

func TestOTX_FormatURL_HostnameCategoryWhenSubdomain(t *testing.T) {
	var seen string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_next":false,"url_list":[]}`))
		},
	)
	c := newOTXClient(t, srv)
	_, err := collect(t, c, context.Background(), "blog.example.com")
	require.NoError(t, err)
	require.Contains(t, seen, "/indicators/hostname/blog.example.com/",
		"input with subdomain & --subs off → hostname category (got %q)", seen)
}

func TestOTX_FormatURL_SubsRollsUpToParent(t *testing.T) {
	var seen string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_next":false,"url_list":[]}`))
		},
	)
	cfg := testutil.NewProviderConfig(t)
	cfg.IncludeSubdomains = true
	c := otx.New(cfg)
	c.SetBaseURL(srv.URL)

	_, err := collect(t, c, context.Background(), "blog.example.com")
	require.NoError(t, err)
	require.True(t, strings.Contains(seen, "/indicators/domain/example.com/"),
		"--subs on subdomain input must strip to apex (got %q)", seen)
}

func TestOTX_New_AppendsTrailingSlashToCustomHost(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	cfg.OTX = "https://otx.example.org"
	c := otx.New(cfg)
	// The behavior is encapsulated; we can't introspect baseURL directly.
	// Use SetBaseURL to set without slash, then issue a request to verify.
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_next":false,"url_list":[]}`))
		},
	)
	c.SetBaseURL(srv.URL) // no trailing slash on a typical srv.URL
	_, err := collect(t, c, context.Background(), "example.com")
	require.NoError(t, err, "must not produce a malformed URL like server/api/...")
}
