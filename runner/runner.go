// Package runner orchestrates the worker pool that runs each provider
// against each input domain.
package runner

import (
	"context"
	"fmt"
	"sync"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/commoncrawl"
	"github.com/mr-pmillz/gau/v2/pkg/providers/otx"
	"github.com/mr-pmillz/gau/v2/pkg/providers/urlscan"
	"github.com/mr-pmillz/gau/v2/pkg/providers/wayback"
	"github.com/sirupsen/logrus"
)

// Runner owns the worker pool and the configured set of providers.
type Runner struct {
	sync.WaitGroup

	Providers []providers.Provider
	threads   uint
}

// Init initializes the runner. ctx governs any provider that needs to do work
// during construction (commoncrawl fetches collinfo.json on init).
func (r *Runner) Init(ctx context.Context, c *providers.Config, providerNames []string, filters providers.Filters) error {
	r.threads = c.Threads
	for _, name := range providerNames {
		switch name {
		case "urlscan":
			r.Providers = append(r.Providers, urlscan.New(c))
		case "otx":
			r.Providers = append(r.Providers, otx.New(c))
		case "wayback":
			r.Providers = append(r.Providers, wayback.New(c, filters))
		case "commoncrawl":
			cc, err := commoncrawl.New(ctx, c, filters)
			if err != nil {
				return fmt.Errorf("instantiate commoncrawl: %w", err)
			}
			r.Providers = append(r.Providers, cc)
		default:
			logrus.Warnf("unknown provider %q (skipping)", name)
		}
	}
	return nil
}

// Start spawns r.threads worker goroutines. Each worker pulls Work items from
// workChan until the channel is closed or ctx is cancelled.
func (r *Runner) Start(ctx context.Context, workChan chan Work, results chan string) {
	for i := uint(0); i < r.threads; i++ {
		r.Add(1)
		go func() {
			defer r.Done()
			r.worker(ctx, workChan, results)
		}()
	}
}

// Work is a (domain, provider) pair the worker pool will execute.
type Work struct {
	domain   string
	provider providers.Provider
}

// NewWork constructs a Work item.
func NewWork(domain string, provider providers.Provider) Work {
	return Work{domain: domain, provider: provider}
}

// Do executes this Work item.
func (w *Work) Do(ctx context.Context, results chan string) error {
	return w.provider.Fetch(ctx, w.domain, results)
}

// worker pulls Work items off workChan and executes them, surfacing per-work
// errors as warnings rather than fatal: one provider failing for one domain
// shouldn't kill the run.
func (r *Runner) worker(ctx context.Context, workChan chan Work, results chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case work, ok := <-workChan:
			if !ok {
				return
			}
			if err := work.Do(ctx, results); err != nil {
				logrus.WithField("provider", work.provider.Name()).
					Warnf("%s - %v", work.domain, err)
			}
		}
	}
}
