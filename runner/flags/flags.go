// Package flags defines the CLI flag surface for gau and the precedence
// rules for merging flags with the .gau.toml configuration file.
package flags

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/lynxsecurity/pflag"
	"github.com/lynxsecurity/viper"
	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// URLScanConfig is the urlscan-specific subset of the toml config.
type URLScanConfig struct {
	Host   string `mapstructure:"host"`
	APIKey string `mapstructure:"apikey"`
}

// RateLimitConfig is the [ratelimit] subsection of the toml config. Each value
// is requests-per-second. 0 means "no rate limit" for that provider.
type RateLimitConfig struct {
	Wayback     float64 `mapstructure:"wayback"`
	CommonCrawl float64 `mapstructure:"commoncrawl"`
	OTX         float64 `mapstructure:"otx"`
	URLScan     float64 `mapstructure:"urlscan"`
}

// Default per-provider rate limits, picked to be polite without crippling.
// CommonCrawl has historically been hammered by this tool — a low default is
// the most important fix in this fork.
const (
	DefaultRateWayback     = 1.0
	DefaultRateCommonCrawl = 0.5
	DefaultRateOTX         = 5.0
	DefaultRateURLScan     = 2.0
)

// Config is the merged configuration after flags + .gau.toml.
type Config struct {
	Filters           providers.Filters `mapstructure:"filters"`
	Proxy             string            `mapstructure:"proxy"`
	Threads           uint              `mapstructure:"threads"`
	Timeout           uint              `mapstructure:"timeout"`
	Verbose           bool              `mapstructure:"verbose"`
	MaxRetries        uint              `mapstructure:"retries"`
	IncludeSubdomains bool              `mapstructure:"subdomains"`
	RemoveParameters  bool              `mapstructure:"parameters"`
	Providers         []string          `mapstructure:"providers"`
	Blacklist         []string          `mapstructure:"blacklist"`
	JSON              bool              `mapstructure:"json"`
	URLScan           URLScanConfig     `mapstructure:"urlscan"`
	OTX               string            `mapstructure:"otx"`
	Secure            bool              `mapstructure:"secure"`
	FPCap             uint              `mapstructure:"fpcap"`
	RateLimit         RateLimitConfig   `mapstructure:"ratelimit"`
	Outfile           string            // populated from --o flag
}

// ProviderConfig builds the *providers.Config that the runner consumes.
func (c *Config) ProviderConfig() (*providers.Config, error) {
	var dialer fasthttp.DialFunc

	if c.Proxy != "" {
		parse, err := url.Parse(c.Proxy)
		if err != nil {
			return nil, fmt.Errorf("proxy url: %w", err)
		}
		switch parse.Scheme {
		case "http":
			dialer = fasthttpproxy.FasthttpHTTPDialer(strings.ReplaceAll(c.Proxy, "http://", ""))
		case "socks5":
			dialer = fasthttpproxy.FasthttpSocksDialer(c.Proxy)
		default:
			return nil, fmt.Errorf("unsupported proxy scheme: %s", parse.Scheme)
		}
	}

	pc := &providers.Config{
		Threads:           c.Threads,
		Timeout:           c.Timeout,
		MaxRetries:        c.MaxRetries,
		IncludeSubdomains: c.IncludeSubdomains,
		RemoveParameters:  c.RemoveParameters,
		Client: &fasthttp.Client{
			TLSConfig: &tls.Config{
				// Default insecure preserves historical behavior; --secure
				// flips this. See README for the security tradeoff.
				InsecureSkipVerify: !c.Secure,
			},
			Dial: dialer,
		},
		Providers: c.Providers,
		Output:    c.Outfile,
		JSON:      c.JSON,
		URLScan: providers.URLScan{
			Host:   c.URLScan.Host,
			APIKey: c.URLScan.APIKey,
		},
		OTX:    c.OTX,
		Secure: c.Secure,
		FPCap:  c.FPCap,
		RateLimits: providers.RateLimits{
			Wayback:     c.RateLimit.Wayback,
			CommonCrawl: c.RateLimit.CommonCrawl,
			OTX:         c.RateLimit.OTX,
			URLScan:     c.RateLimit.URLScan,
		},
	}

	log.SetLevel(log.ErrorLevel)
	if c.Verbose {
		log.SetLevel(log.InfoLevel)
	}
	pc.Blacklist = mapset.NewThreadUnsafeSet(c.Blacklist...)
	pc.Blacklist.Add("")
	return pc, nil
}

// Options is the flag-parser owner. It wraps a viper instance and the
// pflag.FlagSet used during this run, so multiple instances can coexist —
// notably so unit tests can construct their own Options without colliding
// with the global pflag.CommandLine.
type Options struct {
	viper *viper.Viper
	flags *pflag.FlagSet
	args  []string
}

// New creates an Options wired to a private pflag.FlagSet that parses
// os.Args[1:]. It is the binary's main entry point.
func New() *Options {
	o := NewFromArgs(os.Args[1:])
	lastOptions = o
	return o
}

// NewFromArgs is the test seam: build an Options from an explicit args slice.
// It does NOT touch the global pflag.CommandLine, so tests can call it
// repeatedly without clashing.
func NewFromArgs(rawArgs []string) *Options {
	v := viper.New()
	fs := pflag.NewFlagSet("gau", pflag.ContinueOnError)
	registerFlags(fs)
	fs.AddGoFlagSet(flag.CommandLine)

	if err := fs.Parse(rawArgs); err != nil {
		log.Fatal(err)
	}
	if err := v.BindPFlags(fs); err != nil {
		log.Fatal(err)
	}

	return &Options{viper: v, flags: fs, args: fs.Args()}
}

// registerFlags installs every CLI flag onto the given FlagSet.
func registerFlags(fs *pflag.FlagSet) {
	fs.String("o", "", "filename to write results to")
	fs.String("config", "", "location of config file (default $HOME/.gau.toml or %USERPROFILE%\\.gau.toml)")
	fs.Uint("threads", 1, "number of workers to spawn")
	fs.Uint("timeout", 45, "timeout (in seconds) for HTTP client")
	fs.Uint("retries", 0, "retries for HTTP client")
	fs.String("proxy", "", "http proxy to use")
	fs.StringSlice("blacklist", []string{}, "list of extensions to skip")
	fs.StringSlice("providers", []string{}, "list of providers to use (wayback,commoncrawl,otx,urlscan)")
	fs.Bool("subs", false, "include subdomains of target domain")
	fs.Bool("fp", false, "remove different parameters of the same endpoint")
	fs.Uint("fp-cap", output.DedupCapDefault, "max --fp dedup entries (0 = unbounded; uses LRU eviction when exceeded)")
	fs.Bool("verbose", false, "show verbose output")
	fs.Bool("json", false, "output as json")

	fs.Bool("secure", false, "verify TLS certificates (default false: insecure for back-compat)")

	fs.Float64("rate-limit-wayback", DefaultRateWayback, "wayback requests per second (0 = unlimited)")
	fs.Float64("rate-limit-commoncrawl", DefaultRateCommonCrawl, "commoncrawl requests per second (0 = unlimited)")
	fs.Float64("rate-limit-otx", DefaultRateOTX, "otx requests per second (0 = unlimited)")
	fs.Float64("rate-limit-urlscan", DefaultRateURLScan, "urlscan requests per second (0 = unlimited)")

	fs.StringSlice("mc", []string{}, "list of status codes to match")
	fs.StringSlice("fc", []string{}, "list of status codes to filter")
	fs.StringSlice("mt", []string{}, "list of mime-types to match")
	fs.StringSlice("ft", []string{}, "list of mime-types to filter")
	fs.String("from", "", "fetch urls from date (format: YYYYMM)")
	fs.String("to", "", "fetch urls to date (format: YYYYMM)")
	fs.Bool("version", false, "show gau version")
}

// Args returns positional arguments left after flag parsing.
//
// Deprecated for new code: prefer (*Options).Args(). This package-level
// function exists for back-compat with cmd/gau/main.go and falls back to the
// most-recently-constructed Options' args.
func Args() []string {
	if lastOptions == nil {
		return nil
	}
	return lastOptions.args
}

// (*Options).Args returns the positional arguments parsed by this Options.
func (o *Options) Args() []string { return o.args }

// lastOptions tracks the most recent New() result so the package-level
// Args() can find it. Tests use NewFromArgs and Options.Args directly.
var lastOptions *Options

// ReadInConfig finds and reads the .gau.toml. Missing config is non-fatal.
func (o *Options) ReadInConfig() (*Config, error) {
	confFile := o.viper.GetString("config")

	if confFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o.DefaultConfig(), err
		}

		confFile = filepath.Join(home, ".gau.toml")
	}

	return o.ReadConfigFile(confFile)
}

// ReadConfigFile reads a specific config file path.
func (o *Options) ReadConfigFile(name string) (*Config, error) {
	if _, err := os.Stat(name); errors.Is(err, os.ErrNotExist) {
		return o.DefaultConfig(), fmt.Errorf("config file %s not found, using default config", name)
	}

	o.viper.SetConfigFile(name)

	if err := o.viper.ReadInConfig(); err != nil {
		return o.DefaultConfig(), err
	}

	var c Config

	if err := o.viper.Unmarshal(&c); err != nil {
		return o.DefaultConfig(), err
	}

	o.applyFlagOverrides(&c)

	return &c, nil
}

// DefaultConfig returns the baseline config used when no .gau.toml exists.
// Flag values override these defaults via applyFlagOverrides.
func (o *Options) DefaultConfig() *Config {
	c := &Config{
		Filters:           providers.Filters{},
		Proxy:             "",
		Timeout:           45,
		Threads:           1,
		Verbose:           false,
		MaxRetries:        5,
		IncludeSubdomains: false,
		RemoveParameters:  false,
		Providers:         []string{"wayback", "commoncrawl", "otx", "urlscan"},
		Blacklist:         []string{},
		JSON:              false,
		Outfile:           "",
		FPCap:             output.DedupCapDefault,
		RateLimit: RateLimitConfig{
			Wayback:     DefaultRateWayback,
			CommonCrawl: DefaultRateCommonCrawl,
			OTX:         DefaultRateOTX,
			URLScan:     DefaultRateURLScan,
		},
	}

	o.applyFlagOverrides(c)

	return c
}

// applyFlagOverrides copies any flag values explicitly set on the command line
// into the merged Config, overriding both the defaults and the .gau.toml.
func (o *Options) applyFlagOverrides(c *Config) {
	if o.viper.GetBool("version") {
		fmt.Printf("gau version: %s\n", providers.Version)
		os.Exit(0)
	}

	if v := o.viper.GetString("proxy"); v != "" {
		c.Proxy = v
	}

	if v := o.viper.GetString("o"); v != "" {
		c.Outfile = v
	}

	// Use IsSet to distinguish explicit user override from default: pflag
	// always provides a value, but we want the flag to win only when the
	// user typed it.
	if o.isFlagSet("threads") {
		c.Threads = o.viper.GetUint("threads")
	}
	if o.isFlagSet("timeout") {
		c.Timeout = o.viper.GetUint("timeout")
	}
	if o.isFlagSet("retries") {
		c.MaxRetries = o.viper.GetUint("retries")
	}
	if o.isFlagSet("blacklist") {
		c.Blacklist = o.viper.GetStringSlice("blacklist")
	}
	if o.isFlagSet("providers") {
		c.Providers = o.viper.GetStringSlice("providers")
	}
	if o.isFlagSet("subs") {
		c.IncludeSubdomains = o.viper.GetBool("subs")
	}
	if o.isFlagSet("fp") {
		c.RemoveParameters = o.viper.GetBool("fp")
	}
	if o.isFlagSet("fp-cap") {
		c.FPCap = o.viper.GetUint("fp-cap")
	}
	if o.isFlagSet("secure") {
		c.Secure = o.viper.GetBool("secure")
	}
	if o.isFlagSet("rate-limit-wayback") {
		c.RateLimit.Wayback = o.viper.GetFloat64("rate-limit-wayback")
	}
	if o.isFlagSet("rate-limit-commoncrawl") {
		c.RateLimit.CommonCrawl = o.viper.GetFloat64("rate-limit-commoncrawl")
	}
	if o.isFlagSet("rate-limit-otx") {
		c.RateLimit.OTX = o.viper.GetFloat64("rate-limit-otx")
	}
	if o.isFlagSet("rate-limit-urlscan") {
		c.RateLimit.URLScan = o.viper.GetFloat64("rate-limit-urlscan")
	}

	c.JSON = o.viper.GetBool("json")
	c.Verbose = o.viper.GetBool("verbose")

	o.applyFilterFlags(c)
}

// applyFilterFlags reads the date / status / mime filter flags and overrides
// the toml-loaded filters when any one is set on the CLI.
func (o *Options) applyFilterFlags(c *Config) {
	mc := o.viper.GetStringSlice("mc")
	fc := o.viper.GetStringSlice("fc")
	mt := o.viper.GetStringSlice("mt")
	ft := o.viper.GetStringSlice("ft")
	from := o.viper.GetString("from")
	to := o.viper.GetString("to")

	var seen bool
	var f providers.Filters

	if len(mc) > 0 {
		seen = true
		f.MatchStatusCodes = mc
	}
	if len(fc) > 0 {
		seen = true
		f.FilterStatusCodes = fc
	}
	if len(mt) > 0 {
		seen = true
		f.MatchMimeTypes = mt
	}
	if len(ft) > 0 {
		seen = true
		f.FilterMimeTypes = ft
	}
	if from != "" {
		seen = true
		if _, err := time.Parse("200601", from); err == nil {
			f.From = from
		}
	}
	if to != "" {
		seen = true
		if _, err := time.Parse("200601", to); err == nil {
			f.To = to
		}
	}

	if seen {
		c.Filters = f
	}
}

// isFlagSet reports whether the user explicitly typed --flag on the CLI.
func (o *Options) isFlagSet(name string) bool {
	f := o.flags.Lookup(name)
	return f != nil && f.Changed
}
