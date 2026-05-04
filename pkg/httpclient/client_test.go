package httpclient_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/httpclient"
	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"golang.org/x/time/rate"
)

func newClient() *fasthttp.Client {
	return &fasthttp.Client{}
}

func TestMakeRequest_Success(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.JSON(http.StatusOK, `{"ok":true}`))

	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL, httpclient.RequestOpts{Timeout: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(body))
	require.EqualValues(t, 1, srv.Hits())
}

func TestMakeRequest_RetryThenSuccess(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.Status(http.StatusInternalServerError),
		testutil.Status(http.StatusBadGateway),
		testutil.JSON(http.StatusOK, `"third-time-lucky"`),
	)

	start := time.Now()
	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 3, Timeout: 5})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.JSONEq(t, `"third-time-lucky"`, string(body))
	require.EqualValues(t, 3, srv.Hits(), "should hit three times before success")
	// First retry waits ~500ms, second waits ~1s, so total >= 500ms.
	// Generous lower bound to absorb scheduler jitter.
	require.GreaterOrEqual(t, elapsed, 400*time.Millisecond,
		"two retries should each wait the configured backoff (got %s)", elapsed)
}

func TestMakeRequest_RetryExhaustion(t *testing.T) {
	handlers := make([]http.HandlerFunc, 4)
	for i := range handlers {
		handlers[i] = testutil.Status(http.StatusInternalServerError)
	}
	srv := testutil.NewQueueServer(t, handlers...)

	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 3, Timeout: 5})
	require.Error(t, err)
	require.ErrorIs(t, err, httpclient.ErrNon200Response)
	require.EqualValues(t, 4, srv.Hits(), "initial attempt + 3 retries = 4")
}

func TestMakeRequest_BadRequestNoRetry(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.Status(http.StatusBadRequest))

	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 5, Timeout: 5})
	require.ErrorIs(t, err, httpclient.ErrBadRequest)
	require.EqualValues(t, 1, srv.Hits(), "400 must short-circuit, not retry")
}

func TestMakeRequest_AuthFailureNoRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewQueueServer(t, testutil.Status(tc.status))
			_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
				httpclient.RequestOpts{MaxRetries: 5, Timeout: 5})
			require.ErrorIs(t, err, httpclient.ErrAuth)
			require.EqualValues(t, 1, srv.Hits())
		})
	}
}

func TestMakeRequest_RateLimitedHonorsRetryAfter(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		},
		testutil.JSON(http.StatusOK, `"ok"`),
	)

	start := time.Now()
	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 1, Timeout: 5})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.JSONEq(t, `"ok"`, string(body))
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond,
		"Retry-After: 1 must be honored (got %s)", elapsed)
	require.Less(t, elapsed, 3*time.Second,
		"Retry-After: 1 must not greatly overshoot (got %s)", elapsed)
}

func TestMakeRequest_RateLimitedExhaustsAndReturnsErr(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			// No Retry-After — falls back to backoff.
			w.WriteHeader(http.StatusTooManyRequests)
		},
	)

	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 0, Timeout: 5})
	require.ErrorIs(t, err, httpclient.ErrRateLimited)
}

func TestMakeRequest_ContextCancellationAbortsInFlight(t *testing.T) {
	released := make(chan struct{})
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			// Block until the test releases us, simulating a slow upstream.
			<-released
			w.WriteHeader(http.StatusOK)
		},
	)
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := httpclient.MakeRequest(ctx, newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 30})
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, 1*time.Second,
		"ctx cancel must abort wait, not block on the request (got %s)", elapsed)
}

func TestMakeRequest_ContextAlreadyCancelled(t *testing.T) {
	srv := testutil.NewQueueServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := httpclient.MakeRequest(ctx, newClient(), srv.URL, httpclient.RequestOpts{})
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 0, srv.Hits(), "must not even hit the server")
}

func TestMakeRequest_RateLimiter(t *testing.T) {
	hits := atomic.Int64{}
	handlers := make([]http.HandlerFunc, 4)
	for i := range handlers {
		handlers[i] = func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strconv.Itoa(int(hits.Load()))))
		}
	}
	srv := testutil.NewQueueServer(t, handlers...)

	// 10 tokens/sec. 4 calls => ~3 token-waits => ~300ms minimum.
	limiter := rate.NewLimiter(rate.Limit(10), 1)

	start := time.Now()
	for i := 0; i < 4; i++ {
		_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
			httpclient.RequestOpts{Timeout: 5, Limiter: limiter})
		require.NoError(t, err)
	}
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 250*time.Millisecond,
		"limiter at 10/s must space 4 calls by ~300ms (got %s)", elapsed)
}

func TestMakeRequest_ForwardsHeaders(t *testing.T) {
	gotKey := ""
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("API-Key")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		},
	)

	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5},
		httpclient.Header{Key: "API-Key", Value: "secret-token"},
	)
	require.NoError(t, err)
	require.Equal(t, "secret-token", gotKey)
}

func TestMakeRequest_SkipsEmptyHeaderKey(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.JSON(http.StatusOK, `{}`))
	_, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 5},
		httpclient.Header{Key: "", Value: "ignored"},
	)
	require.NoError(t, err)
}

// TestMakeRequest_ZeroTimeoutNoTimeout sanity-checks that timeout=0 doesn't
// trigger an immediate deadline-exceeded.
func TestMakeRequest_ZeroTimeoutNoTimeout(t *testing.T) {
	srv := testutil.NewQueueServer(t, testutil.JSON(http.StatusOK, `"ok"`))
	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{Timeout: 0})
	require.NoError(t, err)
	require.JSONEq(t, `"ok"`, string(body))
}

// Sanity: ensure unique sentinel errors don't satisfy each other's Is.
func TestSentinelsDistinct(t *testing.T) {
	require.False(t, errors.Is(httpclient.ErrAuth, httpclient.ErrBadRequest))
	require.False(t, errors.Is(httpclient.ErrRateLimited, httpclient.ErrNon200Response))
}

// TestMakeRequest_RetryAfterHTTPDate exercises the RFC1123 path of
// parseRetryAfter, which the integer-seconds tests don't cover.
func TestMakeRequest_RetryAfterHTTPDate(t *testing.T) {
	// Past date — Retry-After should clamp to 0 (no extra wait).
	srv := testutil.NewQueueServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "Mon, 01 Jan 2001 00:00:00 GMT")
			w.WriteHeader(http.StatusTooManyRequests)
		},
		testutil.JSON(http.StatusOK, `"ok"`),
	)
	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 1, Timeout: 5})
	require.NoError(t, err)
	require.JSONEq(t, `"ok"`, string(body))
}

// TestMakeRequest_429NoRetryAfterFallsBackToBackoff confirms that without a
// Retry-After header, the 429 path uses exponential backoff.
func TestMakeRequest_429NoRetryAfterFallsBackToBackoff(t *testing.T) {
	srv := testutil.NewQueueServer(t,
		testutil.Status(http.StatusTooManyRequests),
		testutil.JSON(http.StatusOK, `"ok"`),
	)
	start := time.Now()
	body, err := httpclient.MakeRequest(context.Background(), newClient(), srv.URL,
		httpclient.RequestOpts{MaxRetries: 1, Timeout: 5})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.JSONEq(t, `"ok"`, string(body))
	require.GreaterOrEqual(t, elapsed, 400*time.Millisecond,
		"backoff should fire even without Retry-After (got %s)", elapsed)
}
