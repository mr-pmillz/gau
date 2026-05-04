package wayback_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/wayback"
	"github.com/stretchr/testify/require"
)

// drain spins a goroutine that collects from a results channel until it
// closes, so a Fetch() that pushes into a results chan can complete.
func drain(t *testing.T, ch chan string) <-chan []string {
	t.Helper()
	out := make(chan []string, 1)
	go func() {
		var got []string
		for v := range ch {
			got = append(got, v)
		}
		out <- got
	}()
	return out
}

func newWaybackClient(t *testing.T, srv *testutil.QueueServer) *wayback.Client {
	cfg := testutil.NewProviderConfig(t)
	c := wayback.New(cfg, providers.Filters{})
	c.SetBaseURL(srv.URL + "/cdx/search/cdx")
	return c
}

func TestWayback_Fetch_PaginationTerminatesOnEmptyPage(t *testing.T) {
	// Page 0: 2 results; page 1: header only (the historical regression
	// case — the original code looped forever here).
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `[["original"],["https://example.com/a"],["https://example.com/b"]]`),
		testutil.JSON(http.StatusOK, `[["original"]]`),
	)
	c := newWaybackClient(t, srv)

	ch := make(chan string, 8)
	collected := drain(t, ch)

	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(context.Background(), "example.com", ch)
		close(ch)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch hung — empty page must terminate the loop (regression of upstream bug)")
	}

	got := <-collected
	require.Equal(t, []string{"https://example.com/a", "https://example.com/b"}, got)
	require.Equal(t, 0, srv.Remaining(), "both pages must be consumed")
}

func TestWayback_Fetch_TerminatesOnBadRequest(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `[["original"],["https://example.com/a"]]`),
		testutil.Status(http.StatusBadRequest),
	)
	c := newWaybackClient(t, srv)

	ch := make(chan string, 8)
	collected := drain(t, ch)

	go func() {
		_ = c.Fetch(context.Background(), "example.com", ch)
		close(ch)
	}()

	got := <-collected
	require.Equal(t, []string{"https://example.com/a"}, got)
}

func TestWayback_Fetch_ContextCancellation(t *testing.T) {
	releaseFirst := make(chan struct{})
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[["original"]]`))
		},
	)
	defer close(releaseFirst)

	c := newWaybackClient(t, srv)
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
		require.NoError(t, err, "Fetch must return nil on ctx cancellation")
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not respect ctx cancellation")
	}
}

func TestWayback_Fetch_DecodeErrorReturnsErr(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.JSON(http.StatusOK, `not valid json`),
	)
	c := newWaybackClient(t, srv)

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

func TestWayback_Fetch_SubdomainWildcard(t *testing.T) {
	var seenURL string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seenURL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[["original"]]`))
		},
	)
	cfg := testutil.NewProviderConfig(t)
	cfg.IncludeSubdomains = true
	c := wayback.New(cfg, providers.Filters{})
	c.SetBaseURL(srv.URL + "/cdx/search/cdx")

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	_ = c.Fetch(context.Background(), "example.com", ch)
	close(ch)

	require.Contains(t, seenURL, "*.example.com",
		"--subs must wildcard-prefix the domain (got %q)", seenURL)
}

func TestWayback_Fetch_FilterParamsForwarded(t *testing.T) {
	var seenURL string
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			seenURL = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[["original"]]`))
		},
	)
	cfg := testutil.NewProviderConfig(t)
	c := wayback.New(cfg, providers.Filters{
		From:             "202401",
		MatchStatusCodes: []string{"200"},
	})
	c.SetBaseURL(srv.URL + "/cdx/search/cdx")

	ch := make(chan string, 1)
	go func() {
		for range ch {
		}
	}()
	_ = c.Fetch(context.Background(), "example.com", ch)
	close(ch)

	require.Contains(t, seenURL, "from=202401")
	require.True(t, strings.Contains(seenURL, "statuscode%3A200") ||
		strings.Contains(seenURL, "statuscode:200"),
		"filter must forward (got %q)", seenURL)
}
