package flags_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/runner/flags"
	"github.com/stretchr/testify/require"
)

// withConfigFile writes the given TOML to a temp file and returns its path.
func withConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gau.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestDefaultConfig_HasInsecureTLSForBackCompat(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	require.False(t, cfg.Secure, "default must be insecure to preserve historical behavior")

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.True(t, pc.Client.TLSConfig.InsecureSkipVerify,
		"InsecureSkipVerify must be true when Secure=false (back-compat)")
}

func TestDefaultConfig_RateLimitsHaveSensibleDefaults(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	require.Equal(t, flags.DefaultRateWayback, cfg.RateLimit.Wayback)
	require.Equal(t, flags.DefaultRateCommonCrawl, cfg.RateLimit.CommonCrawl)
	require.Equal(t, flags.DefaultRateOTX, cfg.RateLimit.OTX)
	require.Equal(t, flags.DefaultRateURLScan, cfg.RateLimit.URLScan)

	require.Less(t, cfg.RateLimit.CommonCrawl, 1.0,
		"commoncrawl default must be conservative — that's the whole point of the fork")
}

func TestDefaultConfig_FPCapDefault(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	require.EqualValues(t, output.DedupCapDefault, cfg.FPCap)
}

func TestProviderConfig_SecureFlipsTLS(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	cfg.Secure = true

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.False(t, pc.Client.TLSConfig.InsecureSkipVerify,
		"--secure must enable TLS verification")
}

func TestProviderConfig_PropagatesRateLimits(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	cfg.RateLimit.Wayback = 7.5
	cfg.RateLimit.CommonCrawl = 0.25

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.Equal(t, 7.5, pc.RateLimits.Wayback)
	require.Equal(t, 0.25, pc.RateLimits.CommonCrawl)
}

func TestProviderConfig_BlacklistAlwaysContainsEmptyString(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	cfg.Blacklist = []string{"png", "jpg"}

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.True(t, pc.Blacklist.Contains(""),
		"empty string is always added so URLs without an extension don't match")
	require.True(t, pc.Blacklist.Contains("png"))
	require.True(t, pc.Blacklist.Contains("jpg"))
}

func TestProviderConfig_RejectsUnsupportedProxyScheme(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	cfg.Proxy = "ftp://proxy.example/"
	_, err := cfg.ProviderConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported proxy scheme")
}

func TestProviderConfig_AcceptsHTTPProxy(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg := o.DefaultConfig()
	cfg.Proxy = "http://127.0.0.1:8080"
	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.NotNil(t, pc.Client.Dial, "http proxy must wire a dialer")
}

func TestReadConfigFile_MissingFallsBackToDefault(t *testing.T) {
	o := flags.NewFromArgs(nil)
	cfg, err := o.ReadConfigFile("/nonexistent/path/.gau.toml")
	require.Error(t, err, "missing file is signaled but non-fatal")
	require.NotNil(t, cfg)
	require.Equal(t, []string{"wayback", "commoncrawl", "otx", "urlscan"}, cfg.Providers)
}

func TestReadConfigFile_FlagsOverrideTOML(t *testing.T) {
	body := `
threads = 1
secure = false
fpcap = 5
`
	o := flags.NewFromArgs([]string{"--threads", "8", "--secure", "--fp-cap", "999"})
	cfg, err := o.ReadConfigFile(withConfigFile(t, body))
	require.NoError(t, err)
	require.EqualValues(t, 8, cfg.Threads, "--threads must override toml")
	require.True(t, cfg.Secure, "--secure must override toml")
	require.EqualValues(t, 999, cfg.FPCap, "--fp-cap must override toml")
}

func TestReadConfigFile_FilterFlagsOverrideTOML(t *testing.T) {
	body := `
[filters]
from = "202301"
to = "202312"
matchstatuscodes = ["301"]
`
	// CLI override sets new values for from + matchstatuscodes; entire
	// filters block gets replaced.
	o := flags.NewFromArgs([]string{
		"--from", "202401",
		"--to", "202412",
		"--mc", "200,302",
		"--fc", "404",
		"--mt", "text/html",
		"--ft", "image/png",
	})
	cfg, err := o.ReadConfigFile(withConfigFile(t, body))
	require.NoError(t, err)
	require.Equal(t, "202401", cfg.Filters.From)
	require.Equal(t, "202412", cfg.Filters.To)
	require.Equal(t, []string{"200", "302"}, cfg.Filters.MatchStatusCodes)
	require.Equal(t, []string{"404"}, cfg.Filters.FilterStatusCodes)
	require.Equal(t, []string{"text/html"}, cfg.Filters.MatchMimeTypes)
	require.Equal(t, []string{"image/png"}, cfg.Filters.FilterMimeTypes)
}

func TestReadConfigFile_RejectsMalformedDateFlag(t *testing.T) {
	o := flags.NewFromArgs([]string{"--from", "not-a-date"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	// Missing config is signaled but non-fatal.
	require.Error(t, err)
	require.Empty(t, cfg.Filters.From, "malformed --from must be silently dropped")
}

func TestNewFromArgs_RateLimitOverride(t *testing.T) {
	o := flags.NewFromArgs([]string{"--rate-limit-commoncrawl", "0.1"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	require.Error(t, err) // missing file is non-fatal
	require.Equal(t, 0.1, cfg.RateLimit.CommonCrawl)
}

func TestNewFromArgs_ProvidersOverride(t *testing.T) {
	o := flags.NewFromArgs([]string{"--providers", "wayback,otx"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	require.Error(t, err)
	require.Equal(t, []string{"wayback", "otx"}, cfg.Providers)
}

func TestNewFromArgs_BlacklistOverride(t *testing.T) {
	o := flags.NewFromArgs([]string{"--blacklist", "ttf,woff"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	require.Error(t, err)
	require.Equal(t, []string{"ttf", "woff"}, cfg.Blacklist)
}

func TestNewFromArgs_TimeoutAndRetriesOverride(t *testing.T) {
	o := flags.NewFromArgs([]string{"--timeout", "10", "--retries", "20"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	require.Error(t, err)
	require.EqualValues(t, 10, cfg.Timeout)
	require.EqualValues(t, 20, cfg.MaxRetries)
}

func TestNewFromArgs_OutfileFromODash(t *testing.T) {
	o := flags.NewFromArgs([]string{"--o", "/tmp/out.txt"})
	cfg, err := o.ReadConfigFile("/nonexistent")
	require.Error(t, err)
	require.Equal(t, "/tmp/out.txt", cfg.Outfile)
}

func TestOptions_ArgsReturnsPositionals(t *testing.T) {
	o := flags.NewFromArgs([]string{"--threads", "2", "example.com", "second.com"})
	require.Equal(t, []string{"example.com", "second.com"}, o.Args())
}

func TestReadInConfig_FallsBackWhenHomeMissing(t *testing.T) {
	o := flags.NewFromArgs(nil)
	// $HOME/.gau.toml almost certainly doesn't exist for the test process,
	// but even if it does, we shouldn't blow up.
	_, _ = o.ReadInConfig()
	// Just exercise the path; no assertion beyond "doesn't panic".
}

func TestReadConfigFile_ParsesCustomValues(t *testing.T) {
	body := `
threads = 4
verbose = true
retries = 7
secure = true
fpcap = 50000
providers = ["wayback", "otx"]
blacklist = ["png", "gif"]

[ratelimit]
wayback = 2.5
commoncrawl = 0.1
otx = 10
urlscan = 3
`
	o := flags.NewFromArgs(nil)
	cfg, err := o.ReadConfigFile(withConfigFile(t, body))
	require.NoError(t, err)
	require.EqualValues(t, 4, cfg.Threads)
	require.True(t, cfg.Verbose)
	require.EqualValues(t, 7, cfg.MaxRetries)
	require.True(t, cfg.Secure)
	require.EqualValues(t, 50000, cfg.FPCap)
	require.Equal(t, []string{"wayback", "otx"}, cfg.Providers)
	require.Equal(t, []string{"png", "gif"}, cfg.Blacklist)
	require.Equal(t, 2.5, cfg.RateLimit.Wayback)
	require.Equal(t, 0.1, cfg.RateLimit.CommonCrawl)
	require.Equal(t, 10.0, cfg.RateLimit.OTX)
	require.Equal(t, 3.0, cfg.RateLimit.URLScan)
}
