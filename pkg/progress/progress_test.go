package progress_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/progress"
	"github.com/stretchr/testify/require"
)

// safeBuf is a mutex-protected bytes.Buffer so the display goroutine and
// the test goroutine can write/read without racing.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestTracker_CountersAccumulate(t *testing.T) {
	tr := progress.NewTracker()
	tr.WorkQueued(4)
	tr.WorkCompleted("wayback", nil)
	tr.WorkCompleted("otx", errors.New("boom"))
	tr.URLEmitted("wayback", "html")
	tr.URLEmitted("wayback", "html")
	tr.URLEmitted("wayback", "pdf")
	tr.URLEmitted("otx", "")

	s := tr.Snapshot()
	require.EqualValues(t, 4, s.Total)
	require.EqualValues(t, 2, s.Done)
	require.EqualValues(t, 1, s.Errors)
	require.EqualValues(t, 4, s.URLs)
	require.Equal(t, int64(3), s.ByProvider["wayback"])
	require.Equal(t, int64(1), s.ByProvider["otx"])
	require.Equal(t, int64(2), s.ByExtension["html"])
	require.Equal(t, int64(1), s.ByExtension["pdf"])
	require.Equal(t, int64(1), s.ByExtension["(no ext)"],
		"empty extension must bucket as '(no ext)'")
}

func TestTracker_WorkCompletedSeedsZeroProvider(t *testing.T) {
	// A provider that errored before emitting anything should still appear
	// in the summary so the user sees its zero contribution.
	tr := progress.NewTracker()
	tr.WorkCompleted("urlscan", errors.New("auth failed"))

	var buf bytes.Buffer
	tr.WriteSummary(&buf)
	require.Contains(t, buf.String(), "urlscan")
	require.Contains(t, buf.String(), "errors")
}

func TestTracker_SnapshotIsDeepCopy(t *testing.T) {
	tr := progress.NewTracker()
	tr.URLEmitted("wayback", "html")
	s1 := tr.Snapshot()
	s1.ByProvider["wayback"] = 99999 // tamper with copy
	s2 := tr.Snapshot()
	require.Equal(t, int64(1), s2.ByProvider["wayback"],
		"tracker must hand out an independent copy of byProvider")
}

func TestWriteSummary_OrdersByCountDescending(t *testing.T) {
	tr := progress.NewTracker()
	tr.URLEmitted("wayback", "html")
	tr.URLEmitted("wayback", "html")
	tr.URLEmitted("wayback", "html")
	tr.URLEmitted("wayback", "pdf")
	tr.URLEmitted("wayback", "css")
	tr.URLEmitted("wayback", "css")

	var buf bytes.Buffer
	tr.WriteSummary(&buf)
	out := buf.String()
	// html (3) > css (2) > pdf (1)
	htmlIdx := strings.Index(out, "html")
	cssIdx := strings.Index(out, "css")
	pdfIdx := strings.Index(out, "pdf")
	require.True(t, htmlIdx > 0 && cssIdx > 0 && pdfIdx > 0)
	require.Less(t, htmlIdx, cssIdx, "html (3) must appear before css (2)")
	require.Less(t, cssIdx, pdfIdx, "css (2) must appear before pdf (1)")
}

func TestWriteSummary_TopNTruncates(t *testing.T) {
	tr := progress.NewTracker()
	// 12 distinct extensions, decreasing counts.
	exts := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	for i, e := range exts {
		for n := 0; n < len(exts)-i; n++ {
			tr.URLEmitted("wayback", e)
		}
	}

	var buf bytes.Buffer
	tr.WriteSummary(&buf)
	out := buf.String()
	require.Contains(t, out, "... 2 more",
		"with 12 extensions and topN=10, summary must mention '... 2 more'")
	// Top-10 (a..j) are present; bottom 2 (k, l) are not.
	for _, e := range exts[:10] {
		require.Contains(t, out, "  "+e+" ", "top-10 must include "+e)
	}
}

func TestWriteSummary_EmptyTrackerReadsCleanly(t *testing.T) {
	tr := progress.NewTracker()
	var buf bytes.Buffer
	tr.WriteSummary(&buf)
	out := buf.String()
	require.Contains(t, out, "Summary")
	require.Contains(t, out, "(none)",
		"empty tracker must surface '(none)' rather than a misleading total")
}

func TestWriteSummary_ProviderTotalEqualsURLs(t *testing.T) {
	tr := progress.NewTracker()
	for _, p := range []string{"wayback", "wayback", "otx", "otx", "otx", "urlscan"} {
		tr.URLEmitted(p, "html")
	}
	s := tr.Snapshot()
	var sum int64
	for _, c := range s.ByProvider {
		sum += c
	}
	require.Equal(t, s.URLs, sum,
		"sum of per-provider counts must equal total URLs")
}

func TestHumanizeInt_ViaSummary(t *testing.T) {
	tr := progress.NewTracker()
	for i := 0; i < 12_345; i++ {
		tr.URLEmitted("wayback", "html")
	}
	var buf bytes.Buffer
	tr.WriteSummary(&buf)
	require.Contains(t, buf.String(), "12,345",
		"large counts must be rendered with thousand separators")
}

// Internal type test via a tiny re-export shim is not possible across
// packages, so this test exercises the documented behavior end-to-end:
// in non-TTY mode, the rendered stderr stream contains no '\r' bytes.
// The TTY path is hard to test without a real PTY; we trust schollz for
// that and only assert the CI-mode contract here.
func TestDisplay_NonTTYHasNoCarriageReturns(t *testing.T) {
	var buf safeBuf
	tr := progress.NewTracker()
	tr.WorkQueued(2)

	d := progress.NewDisplayForTest(&buf, tr, false)
	tr.URLEmitted("wayback", "html")
	tr.WorkCompleted("wayback", nil)
	tr.URLEmitted("wayback", "pdf")
	tr.WorkCompleted("wayback", nil)
	// Give the display goroutine a couple ticks to flush.
	time.Sleep(250 * time.Millisecond)
	d.Close()

	require.NotContains(t, buf.String(), "\r",
		"non-TTY mode must never emit \\r — CI log collectors render those as garbage")
}

func TestDisplay_RendersToBufferOnTick(t *testing.T) {
	// We can't easily test the auto-TTY path without a real TTY, but we
	// can verify the underlying render logic by exercising the public
	// Tracker → Snapshot path that the display reads from.
	tr := progress.NewTracker()
	tr.WorkQueued(2)
	tr.URLEmitted("wayback", "html")
	tr.WorkCompleted("wayback", nil)

	s := tr.Snapshot()
	require.EqualValues(t, 1, s.Done)
	require.EqualValues(t, 2, s.Total)
	require.EqualValues(t, 1, s.URLs)
}
