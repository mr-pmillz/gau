package providers

import (
	"context"
	"regexp"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/valyala/fasthttp"
	"golang.org/x/time/rate"
)

const Version = `2.3.0`

// Provider is a generic interface for all archive fetchers.
type Provider interface {
	Fetch(ctx context.Context, domain string, results chan string) error
	Name() string
}

// URLScan is the urlscan-specific subset of configuration.
type URLScan struct {
	Host   string
	APIKey string
}

// RateLimits captures the per-provider request rate (in requests per second).
// A zero value means no rate-limiting for that provider.
//
// Defaults are picked to be polite without crippling: see the package docs in
// runner/flags for the rationale behind each number.
type RateLimits struct {
	Wayback     float64
	CommonCrawl float64
	OTX         float64
	URLScan     float64
}

// Limiter returns a *rate.Limiter for the given rate, or nil if rate <= 0.
// A nil limiter means "no rate limit" — every callsite checks for nil.
func Limiter(r float64) *rate.Limiter {
	if r <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(r), 1)
}

// Config is the shared configuration handed to every provider. It is
// populated by runner/flags from CLI flags and the .gau.toml file.
type Config struct {
	Threads           uint
	Timeout           uint
	MaxRetries        uint
	IncludeSubdomains bool
	RemoveParameters  bool
	Client            *fasthttp.Client
	Providers         []string
	Blacklist         mapset.Set[string]
	Output            string
	JSON              bool
	URLScan           URLScan
	OTX               string

	// Secure, when true, enables TLS verification. Default false preserves
	// the historical insecure-by-default behavior; add --secure to opt in.
	Secure bool

	// FPCap caps the size of the --fp dedup set (entries). 0 means unbounded.
	FPCap uint

	// RateLimits sets per-provider request rates. Zero per-provider value
	// means no rate-limiting for that provider.
	RateLimits RateLimits

	// MatchExtensions, when non-empty, restricts emitted URLs to those whose
	// path ends in (a case-insensitive `.` + one of) these extensions. Use
	// without leading dot. Compound extensions like "tar.gz" are supported.
	MatchExtensions []string

	// MatchRegex, when non-empty, restricts emitted URLs to those matching at
	// least one of these compiled regexes. Match is against the full URL.
	MatchRegex []*regexp.Regexp
}
