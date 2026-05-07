// Package output writes the URLs collected by providers to a writer (stdout
// or a file). It centralizes the filter pipeline applied before emission:
// match-ext (allow-list by extension), match-regex (allow-list by pattern),
// blacklist (deny-list by extension), and --fp dedup.
package output

import (
	"container/list"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	jsoniter "github.com/json-iterator/go"
	"github.com/valyala/bytebufferpool"
)

// JSONResult is the shape of a single line in --json mode.
type JSONResult struct {
	URL string `json:"url"`
}

// dedupCap of zero means unbounded. The runner translates the user-facing
// --fp-cap flag into this argument.
const dedupCapDefault = 1_000_000

// DedupCapDefault is the default --fp-cap when the user doesn't override it.
// Exported for use by runner/flags.
const DedupCapDefault = dedupCapDefault

// WriteOptions bundles the parameters that govern URL emission. It exists
// because there are now five filter inputs and a positional-arg signature
// would be unreadable.
type WriteOptions struct {
	// Blacklist excludes URLs whose path extension (case-insensitive) is
	// in this set. The empty string is always added so URLs with no
	// extension are not blacklisted.
	Blacklist map[string]struct{}

	// MatchExtensions, when non-empty, restricts emission to URLs whose
	// path ends in (`.` + one of) these entries. Lowercased. Compound
	// extensions like "tar.gz" are supported via HasSuffix.
	MatchExtensions []string

	// MatchRegex, when non-empty, restricts emission to URLs matching at
	// least one of these patterns. Match is against the full URL.
	MatchRegex []*regexp.Regexp

	// RemoveParameters dedupes by host+path (the --fp flag).
	RemoveParameters bool

	// DedupCap caps the dedup set; 0 means unbounded.
	DedupCap uint
}

// WriteURLs streams URLs from results to writer, applying the full filter
// pipeline. URLs that fail to parse are skipped silently.
func WriteURLs(writer io.Writer, results <-chan string, opts WriteOptions) error {
	dedup := newLRU(int(opts.DedupCap))
	for result := range results {
		u, err := url.Parse(result)
		if err != nil {
			continue
		}
		if !passesFilters(result, u, opts) {
			continue
		}
		if opts.RemoveParameters {
			key := u.Host + u.Path
			if dedup.contains(key) {
				continue
			}
			dedup.add(key)
		}

		buf := bytebufferpool.Get()
		buf.B = append(buf.B, []byte(result)...)
		buf.B = append(buf.B, '\n')
		_, werr := writer.Write(buf.B)
		bytebufferpool.Put(buf)
		if werr != nil {
			return werr
		}
	}
	return nil
}

// WriteURLsJSON is the JSON variant of WriteURLs. Encoder errors on individual
// records are skipped to match prior behavior — recovering on a per-record
// basis is the right call for a streaming tool.
func WriteURLsJSON(writer io.Writer, results <-chan string, opts WriteOptions) {
	dedup := newLRU(int(opts.DedupCap))
	enc := jsoniter.NewEncoder(writer)
	for result := range results {
		u, err := url.Parse(result)
		if err != nil {
			continue
		}
		if !passesFilters(result, u, opts) {
			continue
		}
		if opts.RemoveParameters {
			key := u.Host + u.Path
			if dedup.contains(key) {
				continue
			}
			dedup.add(key)
		}
		_ = enc.Encode(JSONResult{URL: result})
	}
}

// passesFilters returns true when the URL should be emitted under opts.
// Filter order is most-discriminating first (cheapest rejection):
// match-ext, match-regex, then blacklist.
func passesFilters(rawURL string, u *url.URL, opts WriteOptions) bool {
	if !matchesAnyExt(u, opts.MatchExtensions) {
		return false
	}
	if !matchesAnyRegex(rawURL, opts.MatchRegex) {
		return false
	}
	if isBlacklisted(u, opts.Blacklist) {
		return false
	}
	return true
}

// matchesAnyExt returns true if exts is empty (no filter) or the URL path
// ends in `.` + one of the entries. Compound extensions (`tar.gz`) work
// because we use suffix matching, not path.Ext().
func matchesAnyExt(u *url.URL, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	lower := strings.ToLower(u.Path)
	for _, ext := range exts {
		if strings.HasSuffix(lower, "."+ext) {
			return true
		}
	}
	return false
}

// matchesAnyRegex returns true if patterns is empty or the URL matches at
// least one pattern.
func matchesAnyRegex(rawURL string, patterns []*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, re := range patterns {
		if re.MatchString(rawURL) {
			return true
		}
	}
	return false
}

// isBlacklisted returns true when the URL's path extension is in the
// blacklist. Extension matching is case-insensitive and excludes the
// leading dot. URLs without an extension are never blacklisted.
func isBlacklisted(u *url.URL, blacklist map[string]struct{}) bool {
	if blacklist == nil {
		return false
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
	if ext == "" {
		return false
	}
	_, ok := blacklist[ext]
	return ok
}

// lru is a string-keyed LRU set. lru.cap == 0 means unbounded.
type lru struct {
	cap   int
	ll    *list.List
	index map[string]*list.Element
}

func newLRU(cap int) *lru {
	if cap < 0 {
		cap = 0
	}
	return &lru{
		cap:   cap,
		ll:    list.New(),
		index: make(map[string]*list.Element),
	}
}

func (l *lru) contains(k string) bool {
	if e, ok := l.index[k]; ok {
		l.ll.MoveToFront(e)
		return true
	}
	return false
}

func (l *lru) add(k string) {
	if e, ok := l.index[k]; ok {
		l.ll.MoveToFront(e)
		return
	}
	e := l.ll.PushFront(k)
	l.index[k] = e
	if l.cap > 0 && l.ll.Len() > l.cap {
		oldest := l.ll.Back()
		if oldest != nil {
			delete(l.index, oldest.Value.(string))
			l.ll.Remove(oldest)
		}
	}
}
