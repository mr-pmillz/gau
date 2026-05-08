// Package progress provides an opt-in live progress display and end-of-run
// summary for the gau CLI. It is decoupled from the runner and writer
// packages so library consumers can adopt it independently or skip it.
//
// The live display is rendered by github.com/schollz/progressbar/v3,
// configured to auto-adapt to TTY vs CI logs:
//
//   - TTY: ANSI cursor moves redraw the bar in place, throttled to keep
//     redraws cheap.
//   - Non-TTY (pipes, redirects, CI runners): ANSI codes disabled so each
//     update appends a fresh line — log collectors render this cleanly.
//
// Stats accumulation (counters) is independent of the display: a Tracker
// can be used standalone if a caller only wants a summary.
package progress

import (
	"fmt"
	"io"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Stats is a point-in-time snapshot of a Tracker's counters. Returned by
// value so display goroutines can render without holding the Tracker lock.
type Stats struct {
	Total       int64            // total work items queued (may grow in stdin mode)
	Done        int64            // completed work items (provider × domain)
	Errors      int64            // work items whose provider returned an error
	URLs        int64            // URLs emitted post-filter (sum of ByProvider)
	ByProvider  map[string]int64 // emitted URLs per provider
	ByExtension map[string]int64 // emitted URLs per file extension; "(no ext)" bucket
	Started     time.Time
}

// Elapsed returns the rounded duration since Started.
func (s Stats) Elapsed() time.Duration {
	return time.Since(s.Started).Round(time.Second)
}

// Tracker accumulates counters for the run. Safe for concurrent use.
type Tracker struct {
	mu          sync.Mutex
	total       int64
	done        int64
	errors      int64
	urls        int64
	byProvider  map[string]int64
	byExtension map[string]int64
	started     time.Time
}

// NewTracker returns a Tracker with started=now and empty counters.
func NewTracker() *Tracker {
	return &Tracker{
		byProvider:  map[string]int64{},
		byExtension: map[string]int64{},
		started:     time.Now(),
	}
}

// WorkQueued increments the total. Called once per (domain × provider)
// item the runner is about to process. May be called multiple times in
// stdin mode as new domains arrive.
func (t *Tracker) WorkQueued(n int) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.total += int64(n)
	t.mu.Unlock()
}

// WorkCompleted increments the done counter and seeds byProvider so the
// provider appears in the summary even if it emitted zero URLs.
func (t *Tracker) WorkCompleted(provider string, err error) {
	t.mu.Lock()
	t.done++
	if err != nil {
		t.errors++
	}
	if _, ok := t.byProvider[provider]; !ok {
		t.byProvider[provider] = 0
	}
	t.mu.Unlock()
}

// URLEmitted records one URL that survived all filters. ext is the
// path extension (lowercased, no leading dot, "" → "(no ext)").
func (t *Tracker) URLEmitted(provider, ext string) {
	if ext == "" {
		ext = "(no ext)"
	}
	t.mu.Lock()
	t.urls++
	t.byProvider[provider]++
	t.byExtension[ext]++
	t.mu.Unlock()
}

// Snapshot returns a deep copy of the current counters.
func (t *Tracker) Snapshot() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	bp := make(map[string]int64, len(t.byProvider))
	maps.Copy(bp, t.byProvider)
	be := make(map[string]int64, len(t.byExtension))
	maps.Copy(be, t.byExtension)
	return Stats{
		Total: t.total, Done: t.done, Errors: t.errors, URLs: t.urls,
		ByProvider: bp, ByExtension: be, Started: t.started,
	}
}

// WriteSummary writes a human-readable summary block to w.
func (t *Tracker) WriteSummary(w io.Writer) {
	// Stderr writes don't surface useful errors; capture and discard.
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	pln := func(args ...any) { _, _ = fmt.Fprintln(w, args...) }

	s := t.Snapshot()
	p("\n=== Summary (elapsed %s) ===\n\n", s.Elapsed())

	// Per-provider table.
	pln("Per provider:")
	providers := sortedKeys(s.ByProvider)
	const minProviderWidth = 11 // "commoncrawl"
	nameWidth := minProviderWidth
	for _, name := range providers {
		if len(name) > nameWidth {
			nameWidth = len(name)
		}
	}
	var providerTotal int64
	for _, name := range providers {
		c := s.ByProvider[name]
		providerTotal += c
		p("  %-*s %12s\n", nameWidth, name, humanizeInt(c))
	}
	if len(providers) > 0 {
		p("  %s\n", strings.Repeat("─", nameWidth+13))
	}
	p("  %-*s %12s\n", nameWidth, "total", humanizeInt(providerTotal))
	if s.Errors > 0 {
		p("  %-*s %12s\n", nameWidth, "errors", humanizeInt(s.Errors))
	}

	// Top extensions.
	pln()
	pln("Top extensions:")
	const topN = 10
	const maxExtWidth = 16
	type extEntry struct {
		ext   string
		count int64
	}
	entries := make([]extEntry, 0, len(s.ByExtension))
	for k, v := range s.ByExtension {
		entries = append(entries, extEntry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].ext < entries[j].ext
	})
	shown := entries
	if len(shown) > topN {
		shown = shown[:topN]
	}
	extWidth := 8
	for _, e := range shown {
		if l := len(e.ext); l > extWidth {
			extWidth = l
		}
	}
	if extWidth > maxExtWidth {
		extWidth = maxExtWidth
	}
	for _, e := range shown {
		name := e.ext
		if len(name) > extWidth {
			name = name[:extWidth-1] + "…"
		}
		p("  %-*s %12s\n", extWidth, name, humanizeInt(e.count))
	}
	if len(entries) > topN {
		p("  ... %d more\n", len(entries)-topN)
	}
	if len(entries) == 0 {
		pln("  (none)")
	}
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// humanizeInt formats n with thousand-separator commas. Negative input is
// returned unmodified — counters are non-negative by construction.
func humanizeInt(n int64) string {
	if n < 0 {
		return strconv.FormatInt(n, 10)
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// Display is a self-driving progress UI backed by schollz/progressbar/v3.
// NewDisplay starts a goroutine that pulls Snapshot() from the tracker on
// a ticker and pushes counter updates into the bar; Close stops the
// goroutine, finishes the bar, and ensures stderr is left on a clean
// line so the summary block renders cleanly underneath.
type Display struct {
	tracker *Tracker
	bar     *progressbar.ProgressBar
	w       io.Writer
	isTTY   bool
	stop    chan struct{}
	done    chan struct{}
}

// NewDisplay starts a Display that auto-adapts to TTY vs non-TTY. Shell
// redirects (`gau ... 2>file`) and CI runners get an ANSI-free, throttled
// rendering — each update is its own line, never overwriting.
func NewDisplay(stderr *os.File, t *Tracker) *Display {
	return newDisplayWithMode(stderr, t, isTerminal(stderr))
}

func newDisplayWithMode(w io.Writer, t *Tracker, isTTY bool) *Display {
	// Render cadence: TTY can afford 200ms redraws (cheap with ANSI
	// in-place rewriting); non-TTY prints a fresh line each tick, so we
	// throttle to 5s to avoid log spam.
	throttle := 5 * time.Second
	if isTTY {
		throttle = 200 * time.Millisecond
	}

	// schollz/progressbar emits a leading `\r` on every frame even with
	// ANSI codes disabled. That redraws nicely on a TTY but produces
	// undefined behavior in CI log collectors (some show only the last
	// frame, some print garbage). In non-TTY mode we translate `\r` to
	// `\n` so each frame becomes its own clean line.
	barWriter := w
	if !isTTY {
		barWriter = crToNewline{w: w}
	}

	opts := []progressbar.Option{
		progressbar.OptionSetWriter(barWriter),
		progressbar.OptionSetDescription("scanning"),
		progressbar.OptionShowCount(),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionThrottle(throttle),
		progressbar.OptionUseANSICodes(isTTY),
		progressbar.OptionShowDescriptionAtLineEnd(),
		progressbar.OptionOnCompletion(func() {
			if isTTY {
				_, _ = fmt.Fprintln(w)
			}
		}),
	}

	// Start with an unknown max (stdin mode); we ChangeMax64 as work
	// queues. Args mode calls WorkQueued before Start, so the first
	// tick after that will have the real total.
	bar := progressbar.NewOptions64(-1, opts...)

	d := &Display{
		tracker: t,
		bar:     bar,
		w:       w,
		isTTY:   isTTY,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *Display) run() {
	defer close(d.done)
	// Internal poll cadence is faster than the bar's render throttle —
	// we want to keep its counters fresh; it decides when to actually
	// draw based on OptionThrottle.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.update(d.tracker.Snapshot())
		}
	}
}

func (d *Display) update(s Stats) {
	if s.Total > 0 {
		d.bar.ChangeMax64(s.Total)
	}
	d.bar.Describe(fmt.Sprintf("scanning · %s URLs", humanizeInt(s.URLs)))
	_ = d.bar.Set64(s.Done)
}

// Close stops the display goroutine, finalizes the bar, and ensures the
// terminal cursor lands on a fresh line so the end-of-run summary
// renders cleanly. Idempotent.
func (d *Display) Close() {
	select {
	case <-d.stop:
		return // already closed
	default:
		close(d.stop)
	}
	<-d.done
	// Final update with the latest snapshot before finishing.
	d.update(d.tracker.Snapshot())
	_ = d.bar.Finish()
	if !d.isTTY {
		// Non-TTY: Finish doesn't always emit a trailing newline.
		_, _ = fmt.Fprintln(d.w)
	}
}

// crToNewline translates carriage returns to newlines on the way through
// to the underlying writer. Used in non-TTY mode to convert
// schollz/progressbar's in-place `\r`-prefixed frames into one-line-per-
// frame output that CI log collectors render cleanly.
type crToNewline struct {
	w io.Writer
}

func (c crToNewline) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	out := make([]byte, len(p))
	for i, b := range p {
		if b == '\r' {
			out[i] = '\n'
			continue
		}
		out[i] = b
	}
	if _, err := c.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// isTerminal reports whether f is a character device (i.e. a TTY). Pure
// stdlib — avoids a dependency on golang.org/x/term.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
