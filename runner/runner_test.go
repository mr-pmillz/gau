package runner_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/internal/testutil"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/runner"
	"github.com/stretchr/testify/require"
)

const (
	providerWayback = "wayback"
	providerOTX     = "otx"
	providerURLScan = "urlscan"
	testDomain      = "example.com"
	testURLA        = "https://example.com/a"
)

// fakeProvider is a controllable provider for runner tests.
type fakeProvider struct {
	name   string
	urls   []string
	hangCh <-chan struct{}
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Fetch(ctx context.Context, _ string, results chan string) error {
	if p.hangCh != nil {
		select {
		case <-p.hangCh:
		case <-ctx.Done():
			return nil
		}
	}
	for _, u := range p.urls {
		select {
		case <-ctx.Done():
			return nil
		case results <- u:
		}
	}
	return nil
}

func TestRunner_Init_RegistersKnownProviders(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	r := &runner.Runner{}
	err := r.Init(context.Background(), cfg, []string{providerWayback, providerOTX, providerURLScan}, providers.Filters{})
	require.NoError(t, err)
	require.Len(t, r.Providers, 3)
}

func TestRunner_Init_SkipsUnknownProvider(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	r := &runner.Runner{}
	err := r.Init(context.Background(), cfg, []string{providerWayback, "made-up", providerURLScan}, providers.Filters{})
	require.NoError(t, err)
	require.Len(t, r.Providers, 2, "unknown provider names must be silently skipped")
}

func TestRunner_StartSpawnsThreadsWorkers(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	cfg.Threads = 3
	r := &runner.Runner{}
	require.NoError(t, r.Init(context.Background(), cfg, []string{providerWayback}, providers.Filters{}))

	provider := &fakeProvider{name: "fake", urls: []string{testURLA}}
	r.Providers = []providers.Provider{provider}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := make(chan runner.Work, 3)
	results := make(chan runner.Result, 16)

	r.Start(ctx, work, results)

	// Submit one item per worker.
	for i := 0; i < 3; i++ {
		work <- runner.NewWork(testDomain, provider)
	}
	close(work)
	r.Wait()
	close(results)

	var got []runner.Result
	for v := range results {
		got = append(got, v)
	}
	require.Len(t, got, 3, "3 workers × 1 URL each should emit 3 results")
	for _, r := range got {
		require.Equal(t, testURLA, r.URL)
		require.Equal(t, "fake", r.Provider,
			"runner must tag every Result with the producing provider's name")
	}
}

func TestRunner_ContextCancellationStopsWorkers(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	cfg.Threads = 2
	r := &runner.Runner{}
	require.NoError(t, r.Init(context.Background(), cfg, []string{}, providers.Filters{}))

	hang := make(chan struct{})
	defer close(hang)
	provider := &fakeProvider{name: "fake", hangCh: hang}
	r.Providers = []providers.Provider{provider}

	ctx, cancel := context.WithCancel(context.Background())
	work := make(chan runner.Work, 2)
	results := make(chan runner.Result, 1)

	r.Start(ctx, work, results)

	work <- runner.NewWork(testDomain, provider)
	work <- runner.NewWork(testDomain, provider)

	time.Sleep(50 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not exit within 2s after ctx cancel")
	}
	close(work)
}

func TestRunner_OneProviderErrorDoesNotKillRun(t *testing.T) {
	cfg := testutil.NewProviderConfig(t)
	cfg.Threads = 1
	r := &runner.Runner{}
	require.NoError(t, r.Init(context.Background(), cfg, []string{}, providers.Filters{}))

	good := &fakeProvider{name: "good", urls: []string{testURLA}}
	bad := &errProvider{name: "bad"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := make(chan runner.Work, 2)
	results := make(chan runner.Result, 4)

	r.Start(ctx, work, results)
	work <- runner.NewWork(testDomain, bad)
	work <- runner.NewWork(testDomain, good)
	close(work)

	var wg sync.WaitGroup
	wg.Add(1)
	var got []runner.Result
	go func() {
		defer wg.Done()
		for v := range results {
			got = append(got, v)
		}
	}()

	r.Wait()
	close(results)
	wg.Wait()

	require.Len(t, got, 1, "good provider must still produce results even after bad one errors")
	require.Equal(t, testURLA, got[0].URL)
	require.Equal(t, "good", got[0].Provider)
}

func TestRunner_ResultsTaggedPerProvider(t *testing.T) {
	// Two providers in the same run. Each Result must carry the name of
	// the specific provider that emitted its URL — no cross-contamination
	// from the worker's bridge channel.
	cfg := testutil.NewProviderConfig(t)
	cfg.Threads = 2
	r := &runner.Runner{}
	require.NoError(t, r.Init(context.Background(), cfg, []string{}, providers.Filters{}))

	pA := &fakeProvider{name: "alpha", urls: []string{"https://a.example.com/1", "https://a.example.com/2"}}
	pB := &fakeProvider{name: "beta", urls: []string{"https://b.example.com/1"}}
	r.Providers = []providers.Provider{pA, pB}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := make(chan runner.Work, 2)
	results := make(chan runner.Result, 8)

	r.Start(ctx, work, results)
	work <- runner.NewWork(testDomain, pA)
	work <- runner.NewWork(testDomain, pB)
	close(work)
	r.Wait()
	close(results)

	got := map[string]string{}
	for v := range results {
		got[v.URL] = v.Provider
	}
	require.Equal(t, "alpha", got["https://a.example.com/1"])
	require.Equal(t, "alpha", got["https://a.example.com/2"])
	require.Equal(t, "beta", got["https://b.example.com/1"])
}

type errProvider struct{ name string }

func (p *errProvider) Name() string { return p.name }
func (p *errProvider) Fetch(_ context.Context, _ string, _ chan string) error {
	return errStub("intentional")
}

type errStub string

func (e errStub) Error() string { return string(e) }
