package output_test

import (
	"bytes"
	"strings"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/stretchr/testify/require"
)

// pumpWrite spawns a goroutine to call WriteURLs, returns the buffer + a wait
// channel. Caller closes results and reads from done.
func pumpWrite(t *testing.T, blacklist mapset.Set[string], fp bool, cap uint) (*bytes.Buffer, chan string, chan error) {
	t.Helper()
	buf := &bytes.Buffer{}
	ch := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- output.WriteURLs(buf, ch, blacklist, fp, cap)
	}()
	return buf, ch, done
}

func TestWriteURLs_PassesThroughURLs(t *testing.T) {
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, false, 0)
	ch <- "https://example.com/a"
	ch <- "https://example.com/b"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/a\nhttps://example.com/b\n", buf.String())
}

func TestWriteURLs_BlacklistFiltersByExtension(t *testing.T) {
	bl := mapset.NewThreadUnsafeSet[string]("png", "jpg", "")
	buf, ch, done := pumpWrite(t, bl, false, 0)
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
	bl := mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, bl, false, 0)
	ch <- "https://example.com/upper.PNG"
	ch <- "https://example.com/mixed.PnG"
	close(ch)
	require.NoError(t, <-done)
	require.Empty(t, buf.String(),
		"uppercase extensions must match a lowercase blacklist (regression guard for 56bb83f)")
}

func TestWriteURLs_BlacklistEntriesHaveNoLeadingDot(t *testing.T) {
	// Regression guard for the bug fixed in 56bb83f: blacklist entries are
	// stored as "png", and we trim the leading "." before matching.
	bl := mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, bl, false, 0)
	ch <- "https://example.com/x.png"
	close(ch)
	require.NoError(t, <-done)
	require.Empty(t, buf.String())
}

func TestWriteURLs_QueryStringDoesNotInflateExt(t *testing.T) {
	// path.Ext only looks at the URL path, not the query, so this is fine.
	// Lock it in.
	bl := mapset.NewThreadUnsafeSet[string]("png", "")
	buf, ch, done := pumpWrite(t, bl, false, 0)
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
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, false, 0)
	ch <- "://no-scheme"
	ch <- "https://example.com/ok"
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/ok\n", buf.String())
}

func TestWriteURLs_FPDedupesByHostPath(t *testing.T) {
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, true, 100)
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
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, true, 2)
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
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, true, 2)
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
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf, ch, done := pumpWrite(t, bl, true, 0)
	for i := 0; i < 10; i++ {
		ch <- "https://example.com/a"
	}
	close(ch)
	require.NoError(t, <-done)
	require.Equal(t, "https://example.com/a\n", buf.String(),
		"cap=0 means unbounded — should still suppress duplicates")
}

func TestWriteURLs_PropagatesWriteError(t *testing.T) {
	bl := mapset.NewThreadUnsafeSet[string]("")
	w := &errOnceWriter{}
	ch := make(chan string, 4)
	ch <- "https://example.com/a"
	close(ch)
	err := output.WriteURLs(w, ch, bl, false, 0)
	require.Error(t, err)
}

type errOnceWriter struct{ called bool }

func (w *errOnceWriter) Write(p []byte) (int, error) {
	w.called = true
	return 0, errStub
}

var errStub = stubError("boom")

type stubError string

func (e stubError) Error() string { return string(e) }

func TestWriteURLsJSON_OutputsValidJSONLines(t *testing.T) {
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, bl, false, 0)
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
	bl := mapset.NewThreadUnsafeSet[string]("png", "")
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, bl, false, 0)
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
	bl := mapset.NewThreadUnsafeSet[string]("")
	buf := &bytes.Buffer{}
	ch := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		output.WriteURLsJSON(buf, ch, bl, true, 100)
		close(done)
	}()
	ch <- "https://example.com/a?x=1"
	ch <- "https://example.com/a?x=2"
	close(ch)
	<-done
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 1)
}
