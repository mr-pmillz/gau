// Package httpclient is the single HTTP entry point used by every provider.
// It centralizes retry policy, jittered exponential backoff, error
// classification, rate limiting, context propagation, and user-agent
// rotation, so that providers stay thin and consistent.
package httpclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/valyala/fasthttp"
	"golang.org/x/time/rate"
)

// Sentinel errors. Callers can distinguish via errors.Is.
var (
	ErrNilResponse    = errors.New("unexpected nil response")
	ErrNon200Response = errors.New("API responded with non-200 status code")
	ErrBadRequest     = errors.New("API responded with 400 status code")
	ErrAuth           = errors.New("API responded with auth-failure status (401/403)")
	ErrRateLimited    = errors.New("API responded with 429 status code")
)

// Header is a request header the caller wants attached.
type Header struct {
	Key   string
	Value string
}

// RequestOpts carries per-request tuning. Zero values are sensible defaults
// (no retries, no timeout, no rate limit).
type RequestOpts struct {
	// MaxRetries is the number of retries on top of the initial attempt.
	// MaxRetries=0 means a single attempt with no retry.
	MaxRetries uint

	// Timeout is the per-attempt timeout in seconds. 0 means no timeout.
	Timeout uint

	// Limiter, if non-nil, gates each attempt via Limiter.Wait(ctx).
	Limiter *rate.Limiter
}

// Backoff parameters. Exposed for tests.
var (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 30 * time.Second
)

// MakeRequest performs a GET against url, observing ctx, the rate limiter,
// and a jittered exponential backoff retry policy. It is the only HTTP entry
// point used by providers.
//
// The function returns immediately on:
//   - 400 (ErrBadRequest) — wrong query, won't get better
//   - 401/403 (ErrAuth)   — auth failure, won't get better
//   - context cancellation
//
// 429 with Retry-After is honored exactly. Other 5xx and network errors are
// retried with exponential backoff up to MaxRetries times.
func MakeRequest(ctx context.Context, c *fasthttp.Client, url string, opts RequestOpts, headers ...Header) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var lastErr error
	attempts := int(opts.MaxRetries) + 1
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := delayFor(lastErr, attempt)
			if err := sleep(ctx, delay); err != nil {
				return nil, err
			}
		}
		if opts.Limiter != nil {
			if err := opts.Limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}

		body, err := doOnce(ctx, c, url, opts.Timeout, headers)
		if err == nil {
			return body, nil
		}
		lastErr = err

		// Non-retryable errors short-circuit immediately.
		if errors.Is(err, ErrBadRequest) || errors.Is(err, ErrAuth) {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
	return nil, lastErr
}

// delayFor returns how long to wait before the next attempt. If the previous
// error is a 429 with a Retry-After hint, honor it; otherwise jittered
// exponential backoff.
func delayFor(err error, attempt int) time.Duration {
	var rl *rateLimitedError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		if rl.RetryAfter > backoffMax {
			return backoffMax
		}
		return rl.RetryAfter
	}
	return backoffFor(attempt)
}

// backoffFor returns base * 2^(attempt-1) + jitter, capped at backoffMax.
// Jitter is uniform in [0, delay/2). Uses crypto/rand so concurrent callers
// don't share mutable state.
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := backoffBase << (attempt - 1)
	if delay <= 0 || delay > backoffMax {
		delay = backoffMax
	}
	half := int64(delay) / 2
	if half <= 0 {
		return delay
	}
	j, err := rand.Int(rand.Reader, big.NewInt(half+1))
	if err != nil {
		return delay
	}
	return delay + time.Duration(j.Int64())
}

// sleep is a ctx-aware time.Sleep.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// rateLimitedError carries the Retry-After hint forward to delayFor.
type rateLimitedError struct {
	RetryAfter time.Duration
}

func (e *rateLimitedError) Error() string {
	return fmt.Sprintf("%s (retry after %s)", ErrRateLimited.Error(), e.RetryAfter)
}

// Is so callers can errors.Is(err, ErrRateLimited).
func (e *rateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// doOnce performs a single HTTP attempt with ctx-aware abort. fasthttp 1.31
// does not accept a ctx natively, so we run the request in a goroutine and
// select on ctx.Done. If ctx fires, the in-flight goroutine continues until
// its deadline elapses (bounded by Timeout); we just stop waiting on it.
func doOnce(ctx context.Context, c *fasthttp.Client, url string, timeoutSec uint, headers []Header) ([]byte, error) {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod(fasthttp.MethodGet)
	for _, h := range headers {
		if h.Key != "" {
			req.Header.Set(h.Key, h.Value)
		}
	}
	req.Header.Set(fasthttp.HeaderUserAgent, randomUserAgent())
	req.Header.Set("Accept", "*/*")
	req.SetRequestURI(url)

	resp := fasthttp.AcquireResponse()

	type result struct {
		body []byte
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		// Release in this goroutine — guarantees we don't release while the
		// HTTP I/O is still touching them.
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		var err error
		if timeoutSec > 0 {
			deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
			if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
				deadline = dl
			}
			err = c.DoDeadline(req, resp, deadline)
		} else {
			err = c.Do(req, resp)
		}
		if err != nil {
			ch <- result{err: err}
			return
		}
		body, classifyErr := classifyResponse(resp)
		ch <- result{body: body, err: classifyErr}
	}()

	select {
	case <-ctx.Done():
		// Goroutine will finish when the request times out and release its
		// req/resp. We just stop waiting.
		return nil, ctx.Err()
	case r := <-ch:
		return r.body, r.err
	}
}

// classifyResponse maps an HTTP status code to a sentinel error or returns
// the body. The body is copied because resp's underlying buffer is recycled
// when the response is released.
func classifyResponse(resp *fasthttp.Response) ([]byte, error) {
	sc := resp.StatusCode()
	switch sc {
	case fasthttp.StatusOK:
		// fall through to body return below
	case fasthttp.StatusBadRequest:
		return nil, ErrBadRequest
	case fasthttp.StatusUnauthorized, fasthttp.StatusForbidden:
		return nil, ErrAuth
	case fasthttp.StatusTooManyRequests:
		d, _ := parseRetryAfter(string(resp.Header.Peek("Retry-After")))
		return nil, &rateLimitedError{RetryAfter: d}
	default:
		return nil, fmt.Errorf("%w: status=%d", ErrNon200Response, sc)
	}
	if resp.Body() == nil {
		return nil, ErrNilResponse
	}
	body := make([]byte, len(resp.Body()))
	copy(body, resp.Body())
	return body, nil
}

// parseRetryAfter handles both forms (HTTP date and integer seconds).
func parseRetryAfter(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	if seconds, err := strconv.Atoi(v); err == nil {
		if seconds < 0 {
			return 0, nil
		}
		return time.Duration(seconds) * time.Second, nil
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0, nil
		}
		return d, nil
	}
	return 0, fmt.Errorf("could not parse Retry-After: %q", v)
}

// userAgents is a small modern set; rotated per request via crypto/rand.
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_2) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36 Edg/119.0.0.0",
}

func randomUserAgent() string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userAgents))))
	if err != nil {
		return userAgents[0]
	}
	return userAgents[n.Int64()]
}
