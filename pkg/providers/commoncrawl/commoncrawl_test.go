package commoncrawl_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/commoncrawl"
	"github.com/stretchr/testify/require"
)

// scriptedCommonCrawl returns a server whose first hit is the collinfo
// bootstrap (returning a single API URL pointing back at the same server)
// and subsequent hits are the supplied handlers.
func scriptedCommonCrawl(t *testing.T, handlers ...http.HandlerFunc) (*testutil.QueueServer, *commoncrawl.Client) {
	t.Helper()
	// Closure trick: srvPtr is captured by the bootstrap handler before srv
	// exists, then assigned right after. The handler reads it on the first
	// real request, by which point the assignment is done.
	var srvPtr *testutil.QueueServer
	bootstrap := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"cdx-api":"` + srvPtr.URL + `/cdx-api"}]`))
	}
	all := append([]http.HandlerFunc{bootstrap}, handlers...)
	srvPtr = testutil.NewQueueServer(t, all...)

	cfg := testutil.NewProviderConfig(t)
	c, err := commoncrawl.NewWithCollinfoURL(context.Background(), cfg, providers.Filters{}, srvPtr.URL+"/collinfo.json")
	require.NoError(t, err)
	return srvPtr, c
}

func TestCommonCrawl_New_BootstrapErrorPropagates(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.Status(http.StatusInternalServerError))
	cfg := testutil.NewProviderConfig(t)
	_, err := commoncrawl.NewWithCollinfoURL(context.Background(), cfg, providers.Filters{}, srv.URL+"/collinfo.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "fetch collinfo.json")
}

func TestCommonCrawl_New_EmptyIndexErrors(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.JSON(http.StatusOK, `[]`))
	cfg := testutil.NewProviderConfig(t)
	_, err := commoncrawl.NewWithCollinfoURL(context.Background(), cfg, providers.Filters{}, srv.URL+"/collinfo.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no indexes")
}

func TestCommonCrawl_Fetch_ZeroPages(t *testing.T) {
	srv, c := scriptedCommonCrawl(t,
		testutil.JSON(http.StatusOK, `{"pages":0,"blocks":0,"pageSize":5}`),
	)

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	err := c.Fetch(context.Background(), "example.com", ch)
	close(ch)
	require.NoError(t, err)
	require.Equal(t, 0, srv.Remaining(), "only the pagination probe should run")
}

func TestCommonCrawl_Fetch_PaginationStreamsURLs(t *testing.T) {
	body1 := strings.Join([]string{
		`{"url":"https://example.com/a"}`,
		`{"url":"https://example.com/b"}`,
	}, "\n")
	body2 := `{"url":"https://example.com/c"}`

	srv, c := scriptedCommonCrawl(t,
		testutil.JSON(http.StatusOK, `{"pages":2,"blocks":1,"pageSize":2}`),
		testutil.JSON(http.StatusOK, body1),
		testutil.JSON(http.StatusOK, body2),
	)

	ch := make(chan string, 8)
	collected := make(chan []string, 1)
	go func() {
		var got []string
		for v := range ch {
			got = append(got, v)
		}
		collected <- got
	}()

	go func() {
		_ = c.Fetch(context.Background(), "example.com", ch)
		close(ch)
	}()

	got := <-collected
	require.ElementsMatch(t, []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}, got)
	require.Equal(t, 0, srv.Remaining(), "pagination + 2 pages must all be consumed")
}

func TestCommonCrawl_Fetch_APIErrorSurfaces(t *testing.T) {
	_, c := scriptedCommonCrawl(t,
		testutil.JSON(http.StatusOK, `{"pages":1,"pageSize":1}`),
		testutil.JSON(http.StatusOK, `{"error":"index unavailable"}`),
	)

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	err := c.Fetch(context.Background(), "example.com", ch)
	close(ch)
	require.Error(t, err)
	require.Contains(t, err.Error(), "index unavailable")
}

func TestCommonCrawl_Fetch_429TerminatesPagePathWithError(t *testing.T) {
	// First call after pagination probe is rate-limited. With MaxRetries=0
	// in the test config, this surfaces as ErrRateLimited.
	_, c := scriptedCommonCrawl(t,
		testutil.JSON(http.StatusOK, `{"pages":1,"pageSize":1}`),
		testutil.Status(http.StatusTooManyRequests),
	)

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	err := c.Fetch(context.Background(), "example.com", ch)
	close(ch)
	require.Error(t, err)
}

func TestCommonCrawl_Fetch_ContextCancellation(t *testing.T) {
	releaseFirst := make(chan struct{})
	srv, c := scriptedCommonCrawl(t,
		// Pagination probe blocks until released; ctx will cancel during it.
		func(w http.ResponseWriter, r *http.Request) {
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pages":0}`))
		},
	)
	defer close(releaseFirst)
	_ = srv

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(ctx, "example.com", ch)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "ctx cancel should yield nil from Fetch")
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not respect ctx cancellation")
	}
}

func TestCommonCrawl_Fetch_DecodeErrorReturnsErr(t *testing.T) {
	_, c := scriptedCommonCrawl(t,
		testutil.JSON(http.StatusOK, `{"pages":1,"pageSize":1}`),
		testutil.JSON(http.StatusOK, `not valid json line`),
	)

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	err := c.Fetch(context.Background(), "example.com", ch)
	close(ch)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode")
}
