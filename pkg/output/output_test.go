package output_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/stretchr/testify/require"
)

// defaultOpts builds a WriteOptions that does no filtering — every test
// then mutates only the fields it cares about.
func defaultOpts() output.WriteOptions {
	return output.WriteOptions{
		Blacklist: mapset.NewThreadUnsafeSet[string](""),
	}
}

// pumpWrite spawns WriteURLs in a goroutine; the caller pushes URLs into
// the channel and closes it, then reads the buffer + done error.
func pumpWrite(t *testing.T, opts output.WriteOptions) (*bytes.Buffer, chan string, chan error) {
	t.Helper()
	buf := &bytes.Buffer{}
	ch := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- output.WriteURLs(buf, ch, opts)
	}()
	return buf, ch, done
}

func TestWriteURLs_PassesThroughURLs(t *testing.T) {
	buf, ch, done := pumpWrite(t, defaultOpts())
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/a\nhttps://example.com/b\n", buf.String())
}

func TestWriteURLs_BlacklistFiltersByExtension(t *testing.T) {
	opts := defaultOpts()
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "jpg", "")
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/keep.html"
	ch <- "https://example.com/skip.png"
	ch <- "https://example.com/skip.jpg"
	ch <- "https://example.com/keep"
	close(ch)
	require.NoError(t, <-done)
	out := buf.String()
	require.Contains(t, out, "keep.html")
	require.Contains(t, out, "https://example.com/keep\n")
	require.NotContains(t, out, "skip.png")
	require.NotContains(t, out, "skip.jpg")
}

func TestWriteURLs_BlacklistIsCaseInsensitive(t *testing.T) {
	opts := defaultOpts()
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/upper.PNG"
	ch <- "https://example.com/mixed.PnG"
	close(ch)
	require.NoError(t, <-done)
	require.Empty(t, buf.String(),
		"uppercase extensions must match a lowercase blacklist (regression guard for 56bb83f)")
}

func TestWriteURLs_BlacklistEntriesHaveNoLeadingDot(t *testing.T) {
	opts := defaultOpts()
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/x.png"
	close(ch)
	require.NoError(t, <-done)
	require.Empty(t, buf.String())
}

func TestWriteURLs_QueryStringDoesNotInflateExt(t *testing.T) {
	opts := defaultOpts()
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/file.png?width=200"
	ch <- "https://example.com/page.html?download=foo.png"
	close(ch)
	require.NoError(t, <-done)
	require.NotContains(t, buf.String(), "file.png",
		"path-extension match should drop file.png")
	require.Contains(t, buf.String(), "page.html",
		"query-string ext fakes (?...png) must not match the blacklist")
}

func TestWriteURLs_MalformedURLsAreSkippedNotPanicked(t *testing.T) {
	buf, ch, done := pumpWrite(t, defaultOpts())
	ch <- "://no-scheme"
	ch <- "https://example.com/ok"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/ok\n", buf.String())
}

func TestWriteURLs_FPDedupesByHostPath(t *testing.T) {
	opts := defaultOpts()
	opts.RemoveParameters = true
	opts.DedupCap = 100
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/page?id=1"
	ch <- "https://example.com/page?id=2"
	ch <- "https://example.com/page?id=3"
	ch <- "https://example.com/other?id=1"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2, "fp-dedup must collapse same-host+path URLs")
	require.Equal(t, "https://example.com/page?id=1", lines[0])
	require.Equal(t, "https://example.com/other?id=1", lines[1])
}

func TestWriteURLs_FPCapEvictsLRU(t *testing.T) {
	// Cap=2 LRU walkthrough (LRU front→back, front=most-recent):
	//   add /a            -> [a]                     emit /a
	//   add /b            -> [b, a]                  emit /b
	//   add /c (cap=2!)   -> evict /a, LRU=[c, b]    emit /c
	//   add /a (gone)     -> evict /b, LRU=[a, c]    emit /a (re-emits)
	//   add /b (gone)     -> evict /c, LRU=[b, a]    emit /b (re-emits)
	opts := defaultOpts()
	opts.RemoveParameters = true
	opts.DedupCap = 2
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	ch <- "https://example.com/c"
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Equal(t, []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
		"https://example.com/a",
		"https://example.com/b",
	}, lines)
}

func TestWriteURLs_FPCapTouchKeepsRecentInLRU(t *testing.T) {
	// Cap=2: hitting an existing key should "touch" it to MRU position so
	// it's not the next eviction victim.
	//   add /a       -> [a]                   emit /a
	//   add /b       -> [b, a]                emit /b
	//   /a again     -> [a, b] (touch)        suppressed
	//   add /c       -> evict /b, LRU=[c, a]  emit /c
	//   /a again     -> [a, c] (touch)        suppressed
	opts := defaultOpts()
	opts.RemoveParameters = true
	opts.DedupCap = 2
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	ch <- "https://example.com/a?dup=1"
	ch <- "https://example.com/c"
	ch <- "https://example.com/a?dup=2"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Equal(t, []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}, lines)
}

func TestWriteURLs_FPCapZeroIsUnbounded(t *testing.T) {
	opts := defaultOpts()
	opts.RemoveParameters = true
	opts.DedupCap = 0
	buf, ch, done := pumpWrite(t, opts)
	for i := 0; i < 10; i++ {
		ch <- "https://example.com/a"
	}
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/a\n", buf.String(),
		"cap=0 means unbounded — should still suppress duplicates")
}

func TestWriteURLs_PropagatesWriteError(t *testing.T) {
	w := &errOnceWriter{}
	ch := make(chan string, 4)
	ch <- "https://example.com/a"
	close(ch)
	err := output.WriteURLs(w, ch, defaultOpts())
	require.Error(t, err)
}

type errOnceWriter struct{ called bool }

func (w *errOnceWriter) Write(_ []byte) (int, error) {
	w.called = true
	return 0, errStub
}

var errStub = stubError("boom")

type stubError string

func (e stubError) Error() string { return string(e) }

// --- Match-ext tests ---

func TestWriteURLs_MatchExtAllowsOnlyMatching(t *testing.T) {
	opts := defaultOpts()
	opts.MatchExtensions = []string{"sql", "bak", "zip"}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/dump.sql"
	ch <- "https://example.com/db.bak"
	ch <- "https://example.com/archive.zip"
	ch <- "https://example.com/index.html"
	ch <- "https://example.com/page" // no extension — must NOT match an allow-list
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.ElementsMatch(t, []string{
		"https://example.com/dump.sql",
		"https://example.com/db.bak",
		"https://example.com/archive.zip",
	}, lines)
}

func TestWriteURLs_MatchExtIsCaseInsensitive(t *testing.T) {
	opts := defaultOpts()
	opts.MatchExtensions = []string{"sql"}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/DUMP.SQL"
	ch <- "https://example.com/dump.sql"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2, "uppercase-extension paths should still match a lowercase --match-ext entry")
}

func TestWriteURLs_MatchExtSupportsCompoundExtensions(t *testing.T) {
	opts := defaultOpts()
	opts.MatchExtensions = []string{"tar.gz"}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/backup.tar.gz"
	ch <- "https://example.com/backup.gz"
	ch <- "https://example.com/backup.tar"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/backup.tar.gz\n", buf.String(),
		"compound extension must match exactly — neither .gz alone nor .tar should slip through")
}

func TestWriteURLs_MatchExtEmptyMeansNoFilter(t *testing.T) {
	opts := defaultOpts()
	opts.MatchExtensions = nil
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/a"
	ch <- "https://example.com/b.html"
	close(ch)
	require.NoError(t, <-done)
	require.Contains(t, buf.String(), "/a")
	require.Contains(t, buf.String(), "/b.html")
}

func TestWriteURLs_MatchExtComposesWithBlacklist(t *testing.T) {
	// match-ext allows {sql, png}; blacklist excludes png. Only sql passes.
	opts := defaultOpts()
	opts.MatchExtensions = []string{"sql", "png"}
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/dump.sql"
	ch <- "https://example.com/img.png"
	ch <- "https://example.com/page.html"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/dump.sql\n", buf.String())
}

// --- Match-regex tests ---

func TestWriteURLs_MatchRegexAllowsOnlyMatching(t *testing.T) {
	opts := defaultOpts()
	opts.MatchRegex = []*regexp.Regexp{regexp.MustCompile(`/admin`)}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/admin/login"
	ch <- "https://example.com/user/profile"
	ch <- "https://example.com/api/admin"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.ElementsMatch(t, []string{
		"https://example.com/admin/login",
		"https://example.com/api/admin",
	}, lines)
}

func TestWriteURLs_MatchRegexAnyOfMultiplePatterns(t *testing.T) {
	opts := defaultOpts()
	opts.MatchRegex = []*regexp.Regexp{
		regexp.MustCompile(`\.php$`),
		regexp.MustCompile(`\.jsp$`),
	}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/index.php"
	ch <- "https://example.com/login.jsp"
	ch <- "https://example.com/page.html"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.ElementsMatch(t, []string{
		"https://example.com/index.php",
		"https://example.com/login.jsp",
	}, lines)
}

func TestWriteURLs_MatchRegexEmptyMeansNoFilter(t *testing.T) {
	opts := defaultOpts()
	opts.MatchRegex = nil
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/a"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/a\n", buf.String())
}

func TestWriteURLs_MatchRegexCaseSensitiveByDefault(t *testing.T) {
	// Lock in the documented behavior: match-regex is case-sensitive unless
	// the user opts in via the (?i) flag.
	opts := defaultOpts()
	opts.MatchRegex = []*regexp.Regexp{regexp.MustCompile(`Admin`)}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/Admin"
	ch <- "https://example.com/admin"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/Admin\n", buf.String())
}

func TestWriteURLs_MatchRegexRespectsCaseInsensitiveFlag(t *testing.T) {
	opts := defaultOpts()
	opts.MatchRegex = []*regexp.Regexp{regexp.MustCompile(`(?i)admin`)}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/Admin"
	ch <- "https://example.com/ADMIN/dashboard"
	close(ch)
	require.NoError(t, <-done)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
}

func TestWriteURLs_MatchExtAndRegexCompose(t *testing.T) {
	// Both allow-lists must pass (AND, not OR): URL must match an extension
	// AND match a regex.
	opts := defaultOpts()
	opts.MatchExtensions = []string{"php"}
	opts.MatchRegex = []*regexp.Regexp{regexp.MustCompile(`/admin`)}
	buf, ch, done := pumpWrite(t, opts)
	ch <- "https://example.com/admin/index.php" // ✓ both
	ch <- "https://example.com/index.php"       // ✗ no /admin
	ch <- "https://example.com/admin/data.json" // ✗ wrong ext
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/admin/index.php\n", buf.String())
}

// --- JSON variant ---

func TestWriteURLsJSON_OutputsValidJSONLines(t *testing.T) {
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, defaultOpts())
		close(done)
	}()
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	close(ch)
	<-done

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], `"url":"https://example.com/a"`)
	require.Contains(t, lines[1], `"url":"https://example.com/b"`)
}

func TestWriteURLsJSON_AppliesBlacklist(t *testing.T) {
	opts := defaultOpts()
	opts.Blacklist = mapset.NewThreadUnsafeSet[string]("png", "")
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, opts)
		close(done)
	}()
	ch <- "https://example.com/x.png"
	ch <- "https://example.com/y.html"
	close(ch)
	<-done
	require.NotContains(t, buf.String(), "x.png")
	require.Contains(t, buf.String(), "y.html")
}

func TestWriteURLsJSON_AppliesFPDedup(t *testing.T) {
	opts := defaultOpts()
	opts.RemoveParameters = true
	opts.DedupCap = 100
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, opts)
		close(done)
	}()
	ch <- "https://example.com/a?x=1"
	ch <- "https://example.com/a?x=2"
	close(ch)
	<-done
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 1)
}

func TestWriteURLsJSON_AppliesMatchExtAndRegex(t *testing.T) {
	opts := defaultOpts()
	opts.MatchExtensions = []string{"sql"}
	opts.MatchRegex = []*regexp.Regexp{regexp.MustCompile(`/dump`)}
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, opts)
		close(done)
	}()
	ch <- "https://example.com/dump/2024.sql"
	ch <- "https://example.com/static/2024.sql" // wrong regex
	ch <- "https://example.com/dump/2024.png"   // wrong ext
	close(ch)
	<-done
	require.Contains(t, buf.String(), "/dump/2024.sql")
	require.NotContains(t, buf.String(), "static")
	require.NotContains(t, buf.String(), "dump/2024.png")
}
