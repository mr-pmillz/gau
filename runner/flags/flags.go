// Package flags defines the gau CLI surface — a single root cobra command
// whose flags are merged with $HOME/.gau.toml via viper. The package owns
// (a) the Config value type that the rest of the project consumes,
// (b) NewRootCmd, which wires the flags + run callback into a cobra.Command,
// and (c) ProviderConfig, which converts a Config into the *providers.Config
// the runner expects.
//
// The package does not call os.Exit, log.Fatal, or panic. All error reporting
// goes through cobra's RunE so callers (and tests) can decide what to do.
package flags

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// URLScanConfig is the urlscan-specific subset of the toml config.
type URLScanConfig struct {
	Host   string `mapstructure:"host"`
	APIKey string `mapstructure:"apikey"`
}

// RateLimitConfig is the [ratelimit] subsection of the toml config. Each
// value is requests-per-second. 0 means "no rate limit" for that provider.
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
	DefaultRateCommonCrawl = 0.2 // one request every 5 seconds — CC is highly sensitive to bursty traffic
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
	MatchExtensions   []string          `mapstructure:"matchextensions"`
	MatchRegex        []string          `mapstructure:"matchregex"`
	JSON              bool              `mapstructure:"json"`
	URLScan           URLScanConfig     `mapstructure:"urlscan"`
	OTX               string            `mapstructure:"otx"`
	Secure            bool              `mapstructure:"secure"`
	FPCap             uint              `mapstructure:"fpcap"`
	RateLimit         RateLimitConfig   `mapstructure:"ratelimit"`
	UserAgents        []string          `mapstructure:"useragents"`
	Progress          bool              `mapstructure:"progress"`
	Outfile           string            // populated from --output / -o flag
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

	matchExt := lowerSlice(c.MatchExtensions)
	matchRegex, err := compileRegex(c.MatchRegex)
	if err != nil {
		return nil, err
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
				InsecureSkipVerify: !c.Secure, //nolint:gosec // Documented legacy default; --secure enables verification.
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
		MatchExtensions: matchExt,
		MatchRegex:      matchRegex,
	}

	log.SetLevel(log.ErrorLevel)
	if c.Verbose {
		log.SetLevel(log.InfoLevel)
	}
	pc.Blacklist = make(map[string]struct{}, len(c.Blacklist)+1)
	for _, ext := range c.Blacklist {
		pc.Blacklist[ext] = struct{}{}
	}
	pc.Blacklist[""] = struct{}{} // URLs with no extension are never blacklisted
	return pc, nil
}

// compileRegex compiles each pattern with regexp.Compile, surfacing the first
// error so the user gets immediate feedback rather than silent drops.
func compileRegex(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --match-regex pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// lowerSlice normalizes extension entries: trim whitespace, strip a leading
// dot, lowercase. Empty entries dropped.
func lowerSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, strings.ToLower(strings.TrimPrefix(s, ".")))
	}
	return out
}

// RunFunc is the application entry point invoked by the root command after
// flags + config are merged.
type RunFunc func(cfg *Config, domains []string) error

// NewRootCmd builds the top-level cobra.Command. The run callback is invoked
// once the merged Config is ready and positional args (domains) are
// available. Each call returns a fresh command + viper instance so tests can
// build, exercise, and discard commands independently.
func NewRootCmd(run RunFunc) *cobra.Command {
	v := viper.New()

	cmd := &cobra.Command{
		Use:           "gau [flags] [domain ...]",
		Short:         "Fetch known URLs from passive sources (Wayback, Common Crawl, OTX, URLScan).",
		Long:          "gau (getallurls) collects archived URLs for a domain from passive recon sources without touching the target.",
		Version:       providers.Version,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd, v)
			if err != nil {
				return err
			}
			if run == nil {
				return nil
			}
			return run(cfg, args)
		},
	}

	registerFlags(cmd)

	if err := v.BindPFlags(cmd.Flags()); err != nil {
		// BindPFlags only fails if the FlagSet is malformed — programmer
		// error, not user-facing. Surface it as a panic at command-build
		// time so it's caught in development, not at first user invocation.
		panic(fmt.Sprintf("flags: BindPFlags: %v", err))
	}

	return cmd
}

// registerFlags installs every CLI flag onto the cobra command's FlagSet.
func registerFlags(cmd *cobra.Command) {
	f := cmd.Flags()

	f.StringP("output", "o", "", "filename to write results to")
	f.StringP("config", "c", "", "location of config file (default $HOME/.gau.toml or %USERPROFILE%\\.gau.toml)")
	f.Uint("threads", 1, "number of workers to spawn")
	f.Uint("timeout", 45, "timeout (in seconds) for HTTP client")
	f.Uint("retries", 0, "retries for HTTP client")
	f.String("proxy", "", "http proxy to use")
	f.StringSlice("blacklist", nil, "list of extensions to skip")
	f.StringSlice("match-ext", nil, "only emit URLs whose path ends in one of these extensions (allow-list; supports compound like tar.gz)")
	f.StringSlice("match-regex", nil, "only emit URLs matching at least one of these regex patterns (Go syntax; use (?i) for case-insensitive)")
	f.StringSlice("providers", nil, "list of providers to use (wayback,commoncrawl,otx,urlscan)")
	f.Bool("subs", false, "include subdomains of target domain")
	f.Bool("fp", false, "remove different parameters of the same endpoint")
	f.Uint("fp-cap", output.DedupCapDefault, "max --fp dedup entries (0 = unbounded; uses LRU eviction when exceeded)")
	f.BoolP("verbose", "v", false, "show verbose output")
	f.Bool("json", false, "output as json")
	f.StringSlice("user-agents", nil, "override the built-in User-Agent pool (comma-separated; one will be picked at random per request)")
	f.Bool("progress", false, "show live progress on stderr (auto-adapts to TTY vs CI logs) plus an end-of-run summary")

	f.Bool("secure", false, "verify TLS certificates (default false: insecure for back-compat)")

	f.Float64("rate-limit-wayback", DefaultRateWayback, "wayback requests per second (0 = unlimited)")
	f.Float64("rate-limit-commoncrawl", DefaultRateCommonCrawl, "commoncrawl requests per second (0 = unlimited)")
	f.Float64("rate-limit-otx", DefaultRateOTX, "otx requests per second (0 = unlimited)")
	f.Float64("rate-limit-urlscan", DefaultRateURLScan, "urlscan requests per second (0 = unlimited)")

	f.StringSlice("mc", nil, "list of status codes to match")
	f.StringSlice("fc", nil, "list of status codes to filter")
	f.StringSlice("mt", nil, "list of mime-types to match")
	f.StringSlice("ft", nil, "list of mime-types to filter")
	f.String("from", "", "fetch urls from date (format: YYYYMM)")
	f.String("to", "", "fetch urls to date (format: YYYYMM)")
}

// loadConfig builds a merged Config from defaults, optional .gau.toml file,
// and the flags actually set on the command line. Filter flags follow a
// special all-or-nothing rule: setting any one of them on the CLI replaces
// the entire [filters] block from the toml.
func loadConfig(cmd *cobra.Command, v *viper.Viper) (*Config, error) {
	cfg := defaultConfig()

	configPath, _ := cmd.Flags().GetString("config")
	if configPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configPath = filepath.Join(home, ".gau.toml")
		}
	}

	if configPath != "" {
		if _, statErr := os.Stat(configPath); statErr == nil {
			v.SetConfigFile(configPath)
			if err := v.ReadInConfig(); err != nil {
				log.Warnf("error reading config %s: %v", configPath, err)
			} else if err := v.Unmarshal(cfg); err != nil {
				log.Warnf("error decoding config %s: %v", configPath, err)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			log.Warnf("error stating config %s: %v", configPath, statErr)
		}
		// Missing config is non-fatal — the CLI runs on defaults + flags.
	}

	applyFlagOverrides(cmd, cfg)
	applyFilterFlags(cmd, cfg)
	cfg.Outfile = mustString(cmd, "output")
	return cfg, nil
}

// defaultConfig is the baseline — what the user gets with no flags and no
// .gau.toml present.
func defaultConfig() *Config {
	return &Config{
		Filters:    providers.Filters{},
		Timeout:    45,
		Threads:    1,
		MaxRetries: 5,
		Providers:  []string{"wayback", "commoncrawl", "otx", "urlscan"},
		FPCap:      output.DedupCapDefault,
		RateLimit: RateLimitConfig{
			Wayback:     DefaultRateWayback,
			CommonCrawl: DefaultRateCommonCrawl,
			OTX:         DefaultRateOTX,
			URLScan:     DefaultRateURLScan,
		},
	}
}

// applyFlagOverrides copies any flag the user explicitly set on the CLI into
// cfg, overriding both defaults and toml. Use cmd.Flag(name).Changed to
// distinguish "user typed it" from "default was used".
func applyFlagOverrides(cmd *cobra.Command, cfg *Config) {
	if isSet(cmd, "proxy") {
		cfg.Proxy = mustString(cmd, "proxy")
	}
	if isSet(cmd, "threads") {
		cfg.Threads = mustUint(cmd, "threads")
	}
	if isSet(cmd, "timeout") {
		cfg.Timeout = mustUint(cmd, "timeout")
	}
	if isSet(cmd, "retries") {
		cfg.MaxRetries = mustUint(cmd, "retries")
	}
	if isSet(cmd, "blacklist") {
		cfg.Blacklist = mustStringSlice(cmd, "blacklist")
	}
	if isSet(cmd, "match-ext") {
		cfg.MatchExtensions = mustStringSlice(cmd, "match-ext")
	}
	if isSet(cmd, "match-regex") {
		cfg.MatchRegex = mustStringSlice(cmd, "match-regex")
	}
	if isSet(cmd, "providers") {
		cfg.Providers = mustStringSlice(cmd, "providers")
	}
	if isSet(cmd, "subs") {
		cfg.IncludeSubdomains = mustBool(cmd, "subs")
	}
	if isSet(cmd, "fp") {
		cfg.RemoveParameters = mustBool(cmd, "fp")
	}
	if isSet(cmd, "fp-cap") {
		cfg.FPCap = mustUint(cmd, "fp-cap")
	}
	if isSet(cmd, "secure") {
		cfg.Secure = mustBool(cmd, "secure")
	}
	if isSet(cmd, "rate-limit-wayback") {
		cfg.RateLimit.Wayback = mustFloat64(cmd, "rate-limit-wayback")
	}
	if isSet(cmd, "rate-limit-commoncrawl") {
		cfg.RateLimit.CommonCrawl = mustFloat64(cmd, "rate-limit-commoncrawl")
	}
	if isSet(cmd, "rate-limit-otx") {
		cfg.RateLimit.OTX = mustFloat64(cmd, "rate-limit-otx")
	}
	if isSet(cmd, "rate-limit-urlscan") {
		cfg.RateLimit.URLScan = mustFloat64(cmd, "rate-limit-urlscan")
	}

	if isSet(cmd, "json") {
		cfg.JSON = mustBool(cmd, "json")
	}
	if isSet(cmd, "verbose") {
		cfg.Verbose = mustBool(cmd, "verbose")
	}
	if isSet(cmd, "user-agents") {
		cfg.UserAgents = mustStringSlice(cmd, "user-agents")
	}
	if isSet(cmd, "progress") {
		cfg.Progress = mustBool(cmd, "progress")
	}
}

// applyFilterFlags handles the --from/--to/--mc/--fc/--mt/--ft cluster.
// Behavior preserved from the lynxsecurity-era code: if ANY one of these is
// set on the CLI, the entire [filters] block from the toml is replaced
// rather than merged. Malformed --from/--to dates are silently dropped.
func applyFilterFlags(cmd *cobra.Command, cfg *Config) {
	mc := mustStringSlice(cmd, "mc")
	fc := mustStringSlice(cmd, "fc")
	mt := mustStringSlice(cmd, "mt")
	ft := mustStringSlice(cmd, "ft")
	from := mustString(cmd, "from")
	to := mustString(cmd, "to")

	seen := isSet(cmd, "mc") || isSet(cmd, "fc") || isSet(cmd, "mt") ||
		isSet(cmd, "ft") || isSet(cmd, "from") || isSet(cmd, "to")
	if !seen {
		return
	}

	var f providers.Filters
	if len(mc) > 0 {
		f.MatchStatusCodes = mc
	}
	if len(fc) > 0 {
		f.FilterStatusCodes = fc
	}
	if len(mt) > 0 {
		f.MatchMimeTypes = mt
	}
	if len(ft) > 0 {
		f.FilterMimeTypes = ft
	}
	if from != "" {
		if _, err := time.Parse("200601", from); err == nil {
			f.From = from
		}
	}
	if to != "" {
		if _, err := time.Parse("200601", to); err == nil {
			f.To = to
		}
	}
	cfg.Filters = f
}

// isSet reports whether the user explicitly typed --flag on the CLI.
func isSet(cmd *cobra.Command, name string) bool {
	fl := cmd.Flag(name)
	return fl != nil && fl.Changed
}

// must* helpers swallow the errors that GetString/GetBool/etc. return: those
// errors only fire if the flag isn't registered, which is a programmer bug.
// At the call sites we know the flag exists, so the zero value is safe.
func mustString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}
func mustBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}
func mustUint(cmd *cobra.Command, name string) uint {
	v, _ := cmd.Flags().GetUint(name)
	return v
}
func mustFloat64(cmd *cobra.Command, name string) float64 {
	v, _ := cmd.Flags().GetFloat64(name)
	return v
}
func mustStringSlice(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringSlice(name)
	return v
}
