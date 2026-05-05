package flags_test

import (
	"bytes"
	"io"
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

// runWithArgs builds a fresh cobra command with a capturing run callback,
// sets args, and executes. Returns the resolved Config (or nil if RunE
// wasn't reached) and the Execute error. Stdout/stderr are silenced so
// tests don't pollute the run.
func runWithArgs(t *testing.T, args ...string) (*flags.Config, []string, error) {
	t.Helper()
	var captured *flags.Config
	var capturedDomains []string
	cmd := flags.NewRootCmd(func(c *flags.Config, domains []string) error {
		captured = c
		capturedDomains = domains
		return nil
	})
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return captured, capturedDomains, err
}

// runWithConfigFile is a shortcut for tests that point --config at a temp
// toml file plus zero or more extra flags.
func runWithConfigFile(t *testing.T, configPath string, extra ...string) (*flags.Config, error) {
	t.Helper()
	args := append([]string{"--config", configPath}, extra...)
	cfg, _, err := runWithArgs(t, args...)
	return cfg, err
}

// --- Defaults ---

func TestDefaults_HasInsecureTLSForBackCompat(t *testing.T) {
	cfg, _, err := runWithArgs(t)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.False(t, cfg.Secure, "default must be insecure to preserve historical behavior")

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.True(t, pc.Client.TLSConfig.InsecureSkipVerify,
		"InsecureSkipVerify must be true when Secure=false (back-compat)")
}

func TestDefaults_RateLimitsHaveSensibleDefaults(t *testing.T) {
	cfg, _, err := runWithArgs(t)
	require.NoError(t, err)
	require.Equal(t, flags.DefaultRateWayback, cfg.RateLimit.Wayback)
	require.Equal(t, flags.DefaultRateCommonCrawl, cfg.RateLimit.CommonCrawl)
	require.Equal(t, flags.DefaultRateOTX, cfg.RateLimit.OTX)
	require.Equal(t, flags.DefaultRateURLScan, cfg.RateLimit.URLScan)

	require.Less(t, cfg.RateLimit.CommonCrawl, 1.0,
		"commoncrawl default must be conservative — that's the whole point of the fork")
}

func TestDefaults_FPCapDefault(t *testing.T) {
	cfg, _, err := runWithArgs(t)
	require.NoError(t, err)
	require.EqualValues(t, output.DedupCapDefault, cfg.FPCap)
}

func TestDefaults_ProviderListIsAllFour(t *testing.T) {
	cfg, _, err := runWithArgs(t)
	require.NoError(t, err)
	require.Equal(t, []string{"wayback", "commoncrawl", "otx", "urlscan"}, cfg.Providers)
}

// --- ProviderConfig conversion ---

func TestProviderConfig_SecureFlipsTLS(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--secure")
	require.NoError(t, err)

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.False(t, pc.Client.TLSConfig.InsecureSkipVerify, "--secure must enable TLS verification")
}

func TestProviderConfig_PropagatesRateLimits(t *testing.T) {
	cfg, _, err := runWithArgs(t,
		"--rate-limit-wayback", "7.5",
		"--rate-limit-commoncrawl", "0.25",
	)
	require.NoError(t, err)

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.Equal(t, 7.5, pc.RateLimits.Wayback)
	require.Equal(t, 0.25, pc.RateLimits.CommonCrawl)
}

func TestProviderConfig_BlacklistAlwaysContainsEmptyString(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--blacklist", "png,jpg")
	require.NoError(t, err)

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.True(t, pc.Blacklist.Contains(""),
		"empty string is always added so URLs without an extension don't match")
	require.True(t, pc.Blacklist.Contains("png"))
	require.True(t, pc.Blacklist.Contains("jpg"))
}

func TestProviderConfig_RejectsUnsupportedProxyScheme(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--proxy", "ftp://proxy.example/")
	require.NoError(t, err)
	_, err = cfg.ProviderConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported proxy scheme")
}

func TestProviderConfig_AcceptsHTTPProxy(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--proxy", "http://127.0.0.1:8080")
	require.NoError(t, err)
	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.NotNil(t, pc.Client.Dial, "http proxy must wire a dialer")
}

// --- Flag overrides ---

func TestFlagOverride_Threads(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--threads", "8")
	require.NoError(t, err)
	require.EqualValues(t, 8, cfg.Threads)
}

func TestFlagOverride_TimeoutAndRetries(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--timeout", "10", "--retries", "20")
	require.NoError(t, err)
	require.EqualValues(t, 10, cfg.Timeout)
	require.EqualValues(t, 20, cfg.MaxRetries)
}

func TestFlagOverride_Outfile(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--o", "/tmp/out.txt")
	require.NoError(t, err)
	require.Equal(t, "/tmp/out.txt", cfg.Outfile)
}

func TestFlagOverride_RateLimit(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--rate-limit-commoncrawl", "0.1")
	require.NoError(t, err)
	require.Equal(t, 0.1, cfg.RateLimit.CommonCrawl)
}

func TestFlagOverride_ProvidersList(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--providers", "wayback,otx")
	require.NoError(t, err)
	require.Equal(t, []string{"wayback", "otx"}, cfg.Providers)
}

func TestFlagOverride_Blacklist(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--blacklist", "ttf,woff")
	require.NoError(t, err)
	require.Equal(t, []string{"ttf", "woff"}, cfg.Blacklist)
}

func TestFlagOverride_MatchExtParses(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--match-ext", "sql,bak,tar.gz")
	require.NoError(t, err)
	require.Equal(t, []string{"sql", "bak", "tar.gz"}, cfg.MatchExtensions)
}

func TestProviderConfig_MatchExtIsLowercasedAndDotStripped(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--match-ext", ".SQL,BAK, zip ")
	require.NoError(t, err)
	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.Equal(t, []string{"sql", "bak", "zip"}, pc.MatchExtensions,
		"flags must normalize: lowercase, trim leading dot, trim spaces")
}

func TestFlagOverride_MatchRegexParsesAndCompiles(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--match-regex", `/admin,\.php$`)
	require.NoError(t, err)
	require.Equal(t, []string{`/admin`, `\.php$`}, cfg.MatchRegex)

	pc, err := cfg.ProviderConfig()
	require.NoError(t, err)
	require.Len(t, pc.MatchRegex, 2)
	require.True(t, pc.MatchRegex[0].MatchString("https://example.com/admin"))
	require.True(t, pc.MatchRegex[1].MatchString("https://example.com/index.php"))
}

func TestProviderConfig_RejectsInvalidRegex(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--match-regex", "valid,(unclosed")
	require.NoError(t, err)
	_, err = cfg.ProviderConfig()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --match-regex pattern")
	require.Contains(t, err.Error(), "(unclosed")
}

// --- Positional args ---

func TestArgs_PositionalsForwardedToRun(t *testing.T) {
	_, domains, err := runWithArgs(t, "--threads", "2", "example.com", "second.com")
	require.NoError(t, err)
	require.Equal(t, []string{"example.com", "second.com"}, domains)
}

// --- TOML loading ---

func TestConfigFile_MissingFileFallsBackToDefaults(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--config", "/definitely/does/not/exist.toml")
	require.NoError(t, err, "missing --config file is non-fatal")
	require.Equal(t, []string{"wayback", "commoncrawl", "otx", "urlscan"}, cfg.Providers)
}

func TestConfigFile_ParsesCustomValues(t *testing.T) {
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
	cfg, err := runWithConfigFile(t, withConfigFile(t, body))
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

func TestConfigFile_FlagsOverrideTOML(t *testing.T) {
	body := `
threads = 1
secure = false
fpcap = 5
`
	cfg, err := runWithConfigFile(t, withConfigFile(t, body),
		"--threads", "8", "--secure", "--fp-cap", "999")
	require.NoError(t, err)
	require.EqualValues(t, 8, cfg.Threads, "--threads must override toml")
	require.True(t, cfg.Secure, "--secure must override toml")
	require.EqualValues(t, 999, cfg.FPCap, "--fp-cap must override toml")
}

func TestConfigFile_FilterFlagsReplaceTOMLBlock(t *testing.T) {
	// Documented behavior: setting any --from/--to/--mc/--fc/--mt/--ft
	// flag on the CLI replaces the entire [filters] block from the toml.
	body := `
[filters]
from = "202301"
to = "202312"
matchstatuscodes = ["301"]
`
	cfg, err := runWithConfigFile(t, withConfigFile(t, body),
		"--from", "202401",
		"--to", "202412",
		"--mc", "200,302",
		"--fc", "404",
		"--mt", "text/html",
		"--ft", "image/png",
	)
	require.NoError(t, err)
	require.Equal(t, "202401", cfg.Filters.From)
	require.Equal(t, "202412", cfg.Filters.To)
	require.Equal(t, []string{"200", "302"}, cfg.Filters.MatchStatusCodes)
	require.Equal(t, []string{"404"}, cfg.Filters.FilterStatusCodes)
	require.Equal(t, []string{"text/html"}, cfg.Filters.MatchMimeTypes)
	require.Equal(t, []string{"image/png"}, cfg.Filters.FilterMimeTypes)
}

func TestConfigFile_FilterFlagsAbsentPreservesTOML(t *testing.T) {
	// Negative side of the all-or-nothing rule: when no filter flag is on
	// the CLI, the toml [filters] block is preserved unchanged.
	body := `
[filters]
from = "202301"
to = "202312"
matchstatuscodes = ["301"]
`
	cfg, err := runWithConfigFile(t, withConfigFile(t, body))
	require.NoError(t, err)
	require.Equal(t, "202301", cfg.Filters.From)
	require.Equal(t, "202312", cfg.Filters.To)
	require.Equal(t, []string{"301"}, cfg.Filters.MatchStatusCodes)
}

func TestConfigFile_RejectsMalformedDateFlagSilently(t *testing.T) {
	cfg, _, err := runWithArgs(t, "--from", "not-a-date")
	require.NoError(t, err)
	require.Empty(t, cfg.Filters.From, "malformed --from must be silently dropped")
}

func TestConfigFile_MatchFiltersFromTOML(t *testing.T) {
	body := `
matchextensions = ["sql", "bak"]
matchregex = ["/admin", "/api"]
`
	cfg, err := runWithConfigFile(t, withConfigFile(t, body))
	require.NoError(t, err)
	require.Equal(t, []string{"sql", "bak"}, cfg.MatchExtensions)
	require.Equal(t, []string{"/admin", "/api"}, cfg.MatchRegex)
}

// --- Cobra-level behaviors (the bug that motivated this migration) ---

// captureCmdOutput runs cmd and returns stdout, stderr, exec error.
func captureCmdOutput(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := flags.NewRootCmd(func(*flags.Config, []string) error { return nil })
	cmd.SetArgs(args)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestCobra_HelpExitsCleanly(t *testing.T) {
	// Regression guard: the previous parsing layer treated --help as a
	// fatal parse error. Cobra handles --help internally and Execute()
	// returns nil.
	stdout, _, err := captureCmdOutput(t, "--help")
	require.NoError(t, err, "--help must not return an error from Execute")
	require.Contains(t, stdout, "Usage:", "help output must include the usage block")
	require.Contains(t, stdout, "--match-ext", "help output must list our flags")
}

func TestCobra_VersionPrintsVersion(t *testing.T) {
	stdout, _, err := captureCmdOutput(t, "--version")
	require.NoError(t, err)
	require.Contains(t, stdout, "gau version")
}

func TestCobra_UnknownFlagErrors(t *testing.T) {
	_, _, err := captureCmdOutput(t, "--definitely-not-a-flag")
	require.Error(t, err, "unknown flags must error")
	require.Contains(t, err.Error(), "unknown flag")
}
