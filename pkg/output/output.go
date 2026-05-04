// Package output writes the URLs collected by providers to a writer (stdout
// or a file). It centralizes blacklist filtering and --fp dedup.
package output

import (
	"container/list"
	"io"
	"net/url"
	"path"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
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

// WriteURLs streams URLs from results to writer, applying blacklist filtering
// and (when removeParameters is true) dedup keyed by host+path.
//
// dedupCap caps the dedup set to that many entries via LRU eviction, bounding
// memory at the cost of possibly emitting a duplicate when an evicted entry
// is seen again. dedupCap == 0 disables the cap (unbounded).
func WriteURLs(writer io.Writer, results <-chan string, blacklistMap mapset.Set[string], removeParameters bool, dedupCap uint) error {
	dedup := newLRU(int(dedupCap))
	for result := range results {
		u, err := url.Parse(result)
		if err != nil {
			continue
		}
		if isBlacklisted(u, blacklistMap) {
			continue
		}
		if removeParameters {
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

// WriteURLsJSON is the JSON variant of WriteURLs. Errors writing individual
// records are skipped to match prior behavior — encoder.Encode is the only
// failure mode and recovering on a per-record basis is the right call for a
// streaming tool.
func WriteURLsJSON(writer io.Writer, results <-chan string, blacklistMap mapset.Set[string], removeParameters bool, dedupCap uint) {
	dedup := newLRU(int(dedupCap))
	enc := jsoniter.NewEncoder(writer)
	for result := range results {
		u, err := url.Parse(result)
		if err != nil {
			continue
		}
		if isBlacklisted(u, blacklistMap) {
			continue
		}
		if removeParameters {
			key := u.Host + u.Path
			if dedup.contains(key) {
				continue
			}
			dedup.add(key)
		}
		_ = enc.Encode(JSONResult{URL: result})
	}
}

// isBlacklisted returns true when the URL's path extension is in the
// blacklist. Extension matching is case-insensitive and excludes the leading
// dot. URLs without an extension are never blacklisted.
func isBlacklisted(u *url.URL, blacklist mapset.Set[string]) bool {
	if blacklist == nil {
		return false
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
	if ext == "" {
		return false
	}
	return blacklist.Contains(ext)
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

// DedupCapDefault is the default --fp-cap when the user doesn't override it.
// Exported for use by runner/flags.
const DedupCapDefault = dedupCapDefault
