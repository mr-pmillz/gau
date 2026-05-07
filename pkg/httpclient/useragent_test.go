package httpclient_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/httpclient"
	"github.com/stretchr/testify/require"
)

// resetUserAgents restores the package default UA pool. Call via t.Cleanup
// from any test that calls SetUserAgents to keep tests isolated.
func resetUserAgents(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { httpclient.SetUserAgents(nil) })
}

func TestSetUserAgents_AppliesToRequest(t *testing.T) {
	resetUserAgents(t)
	const custom = "TestBot/1.0 (custom-pool)"
	httpclient.SetUserAgents([]string{custom})

	var gotUA string
	srv := testutil.NewQueueServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.Equal(t, custom, gotUA, "request must carry a UA from the custom pool")
}

func TestSetUserAgents_PicksFromMultiEntryPool(t *testing.T) {
	resetUserAgents(t)
	pool := []string{"Bot/A", "Bot/B", "Bot/C"}
	httpclient.SetUserAgents(pool)

	var gotUA string
	srv := testutil.NewQueueServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.Contains(t, pool, gotUA, "picked UA must come from the configured pool")
}

func TestSetUserAgents_EmptyResetsToDefault(t *testing.T) {
	resetUserAgents(t)
	httpclient.SetUserAgents([]string{"Bot/Custom"})
	httpclient.SetUserAgents(nil) // reset

	var gotUA string
	srv := testutil.NewQueueServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.Contains(t, httpclient.DefaultUserAgents(), gotUA,
		"after reset, UA must come from the built-in default pool")
}

func TestSetUserAgents_FiltersEmptyEntries(t *testing.T) {
	resetUserAgents(t)
	// Mix of empty strings and real entries — empties must be dropped, but
	// the call still installs the cleaned subset (not falling through to
	// the default reset).
	httpclient.SetUserAgents([]string{"", "Bot/Only", ""})

	var gotUA string
	srv := testutil.NewQueueServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.Equal(t, "Bot/Only", gotUA)
}

func TestSetUserAgents_AllEmptyStringsResetsToDefault(t *testing.T) {
	resetUserAgents(t)
	httpclient.SetUserAgents([]string{"", "", ""})

	var gotUA string
	srv := testutil.NewQueueServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.Contains(t, httpclient.DefaultUserAgents(), gotUA,
		"all-empty input must be treated as 'reset to defaults', not 'use empty pool'")
}

func TestDefaultUserAgents_ReturnsIndependentCopy(t *testing.T) {
	a := httpclient.DefaultUserAgents()
	require.NotEmpty(t, a, "built-in pool must not be empty")
	b := httpclient.DefaultUserAgents()
	require.True(t, slices.Equal(a, b), "two calls must return equal contents")

	// Mutating the returned slice must not affect subsequent callers.
	a[0] = "MUTATED"
	c := httpclient.DefaultUserAgents()
	require.NotEqual(t, "MUTATED", c[0],
		"DefaultUserAgents must hand out a defensive copy, not a shared backing array")
}
