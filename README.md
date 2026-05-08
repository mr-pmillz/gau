# getallurls (gau)
[![License](https://img.shields.io/badge/license-MIT-_red.svg)](https://opensource.org/licenses/MIT)

getallurls (gau) fetches known URLs from AlienVault's [Open Threat Exchange](https://otx.alienvault.com), the Wayback Machine, Common Crawl, and URLScan for any given domain. Inspired by Tomnomnom's [waybackurls](https://github.com/tomnomnom/waybackurls).

# Resources
- [Usage](#usage)
- [Using gau as a library](#using-gau-as-a-library)
- [Installation](#installation)
- [ohmyzsh note](#ohmyzsh-note)

## Usage:
Examples:

```bash
$ printf example.com | gau
$ cat domains.txt | gau --threads 5
$ gau example.com google.com
$ gau --output example-urls.txt example.com
$ gau --blacklist png,jpg,gif example.com
```

To display the help for the tool use the `-h` flag:

```bash
$ gau -h
```

| Flag                       | Description                                                                                                         | Example                                   |
|----------------------------|---------------------------------------------------------------------------------------------------------------------|-------------------------------------------|
| `--blacklist`              | list of extensions to skip                                                                                          | gau --blacklist ttf,woff,svg,png          |
| `--config`, `-c`           | Use alternate configuration file (default `$HOME/.gau.toml` or `%USERPROFILE%\.gau.toml`)                           | gau -c $HOME/.config/gau.toml             |
| `--fc`                     | list of status codes to filter                                                                                      | gau --fc 404,302                          |
| `--from`                   | fetch urls from date (format: YYYYMM)                                                                               | gau --from 202101                         |
| `--ft`                     | list of mime-types to filter                                                                                        | gau --ft text/plain                       |
| `--fp`                     | remove different parameters of the same endpoint                                                                    | gau --fp                                  |
| `--fp-cap`                 | max --fp dedup entries (0 = unbounded; LRU eviction past the cap)                                                   | gau --fp --fp-cap 100000                  |
| `--json`                   | emit one JSON object per line (JSONL) with `url` and `provider` fields                                              | gau --json                                |
| `--match-ext`              | only emit URLs whose path ends in one of these extensions (allow-list; compound extensions like `tar.gz` supported) | gau --match-ext sql,bak,zip,tar.gz        |
| `--match-regex`            | only emit URLs matching at least one Go regex pattern; use `(?i)` for case-insensitive                              | gau --match-regex '/admin,\.php$'         |
| `--mc`                     | list of status codes to match                                                                                       | gau --mc 200,500                          |
| `--mt`                     | list of mime-types to match                                                                                         | gau --mt text/html,application/json       |
| `--output`, `-o`           | filename to write results to                                                                                        | gau --output out.txt                      |
| `--progress`               | live progress on stderr (TTY bar / CI-friendly lines) + end-of-run summary by provider and extension                | gau --progress example.com                |
| `--providers`              | list of providers to use (wayback,commoncrawl,otx,urlscan)                                                          | gau --providers wayback                   |
| `--proxy`                  | http proxy to use (socks5:// or http://                                                                             | gau --proxy http://proxy.example.com:8080 |
| `--rate-limit-wayback`     | wayback requests per second (0 = unlimited)                                                                         | gau --rate-limit-wayback 0.5              |
| `--rate-limit-commoncrawl` | commoncrawl requests per second (0 = unlimited)                                                                     | gau --rate-limit-commoncrawl 0.25         |
| `--rate-limit-otx`         | otx requests per second (0 = unlimited)                                                                             | gau --rate-limit-otx 1                    |
| `--rate-limit-urlscan`     | urlscan requests per second (0 = unlimited)                                                                         | gau --rate-limit-urlscan 1                |
| `--retries`                | retries for HTTP client                                                                                             | gau --retries 10                          |
| `--secure`                 | verify TLS certificates (default false: insecure, for back-compat)                                                  | gau --secure example.com                  |
| `--timeout`                | timeout (in seconds) for HTTP client                                                                                | gau --timeout 60                          |
| `--subs`                   | include subdomains of target domain                                                                                 | gau example.com --subs                    |
| `--threads`                | number of workers to spawn                                                                                          | gau example.com --threads                 |
| `--to`                     | fetch urls to date (format: YYYYMM)                                                                                 | gau example.com --to 202101               |
| `--user-agents`            | override the built-in User-Agent pool (one is picked at random per request)                                         | gau --user-agents "Mozilla/5.0 ..."       |
| `--verbose`, `-v`          | show verbose output                                                                                                 | gau -v example.com                        |
| `--version`                | show gau version (long form only — `-v` is verbose)                                                                 | gau --version                             |

### Filtering URLs

Filters compose AND-style; a URL must pass every active filter to be emitted.
The pipeline runs in this order (most-discriminating first):

1. **`--match-ext`** allow-list by extension. Compound extensions like
   `tar.gz` work via suffix matching, not last-segment matching.
2. **`--match-regex`** allow-list by Go regex. Use `(?i)admin` for
   case-insensitive matching. Multiple patterns join as OR — a URL passes if
   it matches **any** pattern.
3. **`--blacklist`** deny-list by extension (case-insensitive last-segment
   match).
4. **`--fp`** dedup by host+path, capped via `--fp-cap` (default 1M, LRU
   eviction).

Recon recipes:

```bash
# Backup-file hunt: only emit URLs that look like exposed backups
gau example.com --match-ext sql,bak,zip,tar.gz,7z,rar,gz

# Admin-panel discovery
gau example.com --match-regex '/admin,/dashboard,/manage'

# PHP files under any /api/ path
gau example.com --match-regex '/api/.*\.php$'
```

### Rate limiting

This fork ships with conservative per-provider rate limits enabled by
default. Common Crawl in particular has been hammered by the unmaintained
upstream `gau`, leading them to introduce strict server-side throttling. The
defaults (`commoncrawl=0.2/s` — one request every 5 seconds, `wayback=1/s`,
`otx=5/s`, `urlscan=2/s`) are designed to be polite to those services while
still being usable. Common Crawl in particular is highly sensitive to
bursty traffic; raising this default is not recommended. Set any limit
to `0` to disable for that provider.

### Progress and summary

`--progress` enables a live progress display on **stderr** plus an
end-of-run summary breakdown — useful for long runs where the URL stream
on stdout gives you no feedback. The display uses
[`schollz/progressbar/v3`](https://github.com/schollz/progressbar) and
auto-adapts to the environment:

- **TTY (interactive shell)** — animated bar redraws in place, throttled
  to 200ms.
- **Non-TTY (pipes, CI runners, `2>file`)** — ANSI codes disabled, one
  status line every 5 seconds. No `\r` garbage in CI logs.

Progress and summary always go to stderr, so piping stdout (`gau --json
--progress example.com | jq`) stays clean.

Sample summary block:

```
=== Summary (elapsed 1m23s) ===

Per provider:
  commoncrawl    3,456
  otx            1,200
  urlscan          523
  wayback       12,345
  ──────────────────────────
  total         17,524

Top extensions:
  html           8,432
  php            2,123
  pdf            1,567
  jpg            1,234
  (no ext)         800
  ... 12 more
```

Counts reflect URLs that survived every active filter (`--match-ext`,
`--match-regex`, `--blacklist`, `--fp`) — i.e. what's actually in the
output stream. URLs with no path extension bucket as `(no ext)`. Top
extensions caps at 10; the rest are summarized as `... N more`.

### User-Agents

`gau` rotates a built-in pool of current Chrome / Firefox / Safari / Edge
User-Agent strings — one is picked at random per request. This matters
because Common Crawl and other passive sources silently drop connections
from non-browser UAs (a generic `gau/x.y` string would just hang).

If a specific UA gets blocked, swap in your own list without rebuilding:

```bash
# CLI: comma-separated list
gau --user-agents "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0,Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15" example.com
```

```toml
# .gau.toml: top-level key
useragents = [
  "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
]
```

Empty / unset → built-in pool. Library consumers can call
`httpclient.SetUserAgents([]string{...})` for the same effect, or
`httpclient.DefaultUserAgents()` to inspect the built-in list.

### TLS verification

By default `gau` does **not** verify TLS certificates — this preserves the
historical behavior of the upstream tool. Pass `--secure` (or set
`secure = true` in `.gau.toml`) to enable verification. Verification is the
right choice for a public-internet recon tool; insecure remains the default
only to avoid silently breaking existing users on networks that terminate
TLS in unexpected ways.


## Configuration Files
gau automatically looks for a configuration file at `$HOME/.gau.toml` or`%USERPROFILE%\.gau.toml`. You can point to a different configuration file using the `--config` flag. **If the configuration file is not found, gau will still run with a default configuration, but will output a message to stderr**.

You can specify options and they will be used for every subsequent run of gau. Any options provided via command line flags will override options set in the configuration file.

An example configuration file can be found [here](https://github.com/mr-pmillz/gau/blob/master/.gau.toml)

## Using gau as a library

`gau` is structured as a CLI on top of three reusable Go packages:

| Package                                                                       | What it gives you                                                                                                                 |
|-------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------|
| `github.com/mr-pmillz/gau/v2/pkg/providers`                                   | The `Provider` interface, shared `Config`, `Filters`, and per-provider rate-limit helpers.                                        |
| `github.com/mr-pmillz/gau/v2/pkg/providers/{wayback,commoncrawl,otx,urlscan}` | One client per archive source. Each implements `Provider.Fetch(ctx, domain, results) error`.                                      |
| `github.com/mr-pmillz/gau/v2/runner`                                          | Worker-pool orchestrator: fan domains × providers across N workers and stream results into a single `chan string`.                |
| `github.com/mr-pmillz/gau/v2/pkg/output`                                      | Optional: filter pipeline (`--match-ext`, `--match-regex`, `--blacklist`, `--fp` dedup) and writer for plain-text or JSON output. |

Pull it in with:

```bash
go get github.com/mr-pmillz/gau/v2@latest
```

### Pattern A — single provider, direct use

The fastest path: build a `Config`, instantiate one provider, drain the
results channel yourself. No worker pool, no filtering. Suitable when you
only need one source or you want to apply your own filtering logic
downstream.

```go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/pkg/providers/wayback"
	"github.com/valyala/fasthttp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := &providers.Config{
		Threads:           1,
		Timeout:           45,
		MaxRetries:        3,
		IncludeSubdomains: true,
		Client: &fasthttp.Client{
			TLSConfig: &tls.Config{InsecureSkipVerify: true}, // see TLS verification note below
		},
		RateLimits: providers.RateLimits{Wayback: 1.0}, // 1 req/sec; 0 disables
	}

	client := wayback.New(cfg, providers.Filters{
		// MatchMimeTypes: []string{"text/html"},
		// From: "202401", To: "202412",
	})

	results := make(chan string, 256)
	go func() {
		defer close(results)
		if err := client.Fetch(ctx, "example.com", results); err != nil {
			fmt.Println("fetch:", err)
		}
	}()

	for u := range results {
		fmt.Println(u)
	}
}
```

The exact same pattern works for `otx.New(cfg)`, `urlscan.New(cfg)`, and
`commoncrawl.New(ctx, cfg, filters)` (commoncrawl is the only constructor
that takes a `ctx` because it fetches `collinfo.json` at init time).

### Pattern B — full pipeline (runner + output writer)

Mirrors what the `gau` CLI does internally: a worker pool that fans
domains × providers across N goroutines, plus the same filter +
deduplication pipeline the CLI uses.

```go
package main

import (
	"context"
	"crypto/tls"
	"os"
	"time"

	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/pkg/providers"
	"github.com/mr-pmillz/gau/v2/runner"
	"github.com/valyala/fasthttp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := &providers.Config{
		Threads:           4,
		Timeout:           45,
		MaxRetries:        3,
		IncludeSubdomains: true,
		Client: &fasthttp.Client{
			TLSConfig: &tls.Config{InsecureSkipVerify: true},
		},
		RateLimits: providers.RateLimits{
			Wayback:     1.0,
			CommonCrawl: 0.2, // one every 5s — CC is highly sensitive
			OTX:         5.0,
			URLScan:     2.0,
		},
		Blacklist: map[string]struct{}{"": {}, "png": {}, "jpg": {}, "svg": {}},
	}
	filters := providers.Filters{} // From/To/MatchStatusCodes/etc. all optional

	var r runner.Runner
	if err := r.Init(ctx, cfg, []string{"wayback", "otx"}, filters); err != nil {
		panic(err)
	}

	// Each Result carries the URL plus the producing provider's name —
	// the same data that drives the JSONL `provider` field in CLI mode.
	results := make(chan runner.Result, 1024)
	workCh := make(chan runner.Work)
	r.Start(ctx, workCh, results)

	// Push (domain × provider) work items.
	go func() {
		defer close(workCh)
		for _, domain := range []string{"example.com", "example.org"} {
			for _, p := range r.Providers {
				select {
				case <-ctx.Done():
					return
				case workCh <- runner.NewWork(domain, p):
				}
			}
		}
	}()

	// When all workers finish, close the results channel so the writer ends.
	go func() {
		r.Wait()
		close(results)
	}()

	// Drain through the same filter/dedup pipeline the CLI uses.
	opts := output.WriteOptions{
		Blacklist:        cfg.Blacklist,
		MatchExtensions:  []string{},      // allow-list by extension
		RemoveParameters: true,            // --fp dedup
		DedupCap:         output.DedupCapDefault,
	}
	if err := output.WriteURLs(os.Stdout, results, opts); err != nil {
		panic(err)
	}
}
```

### Notes for library consumers

- **`Config.Client` is mandatory.** Every provider goes through this
  `*fasthttp.Client`. If you want TLS verification, set
  `InsecureSkipVerify: false`; the CLI defaults to insecure for
  back-compat (see [TLS verification](#tls-verification)) — when embedding
  in another tool, prefer secure unless you have a specific reason not to.
- **`Blacklist` always seed with the empty string** (`""`) so URLs that
  have no path extension are not treated as blacklisted. The CLI does this
  automatically; library consumers must do it themselves.
- **Rate limits.** A zero per-provider rate (`0`) disables limiting for
  that provider. `providers.Limiter(r)` is exported if you want to share a
  limiter across calls.
- **Context cancellation propagates.** All `Fetch` implementations honor
  `ctx.Done()` between pages; cancelling the context terminates pagination
  cleanly without an error.
- **Provider names accepted by `runner.Init`:** `"wayback"`,
  `"commoncrawl"`, `"otx"`, `"urlscan"`. Unknown names log a warning and
  are skipped.
- **Stable API surface.** The four `Provider` constructors and the
  `runner` + `output` types are the supported library entry points. The
  `runner/flags` package is CLI plumbing (cobra/viper) — don't import it
  from a library; build a `*providers.Config` directly as shown above.

## Installation:
### From source:
```
$ go install github.com/mr-pmillz/gau/v2/cmd/gau@latest
```
### From github :
```
git clone https://github.com/mr-pmillz/gau.git; \
cd gau/cmd; \
go build; \
sudo mv gau /usr/local/bin/; \
gau --version;
```
### From binary:
You can download the pre-built binaries from the [releases](https://github.com/mr-pmillz/gau/releases/) page and then move them into your $PATH.

```bash
$ tar xvf gau_2.0.6_linux_amd64.tar.gz
$ mv gau /usr/bin/gau
```

### From Docker:
You can run gau via docker like so:
```bash
docker run --rm sxcurity/gau:latest --help
```


You can also build a docker image with the following command
```bash
docker build -t gau .
```
and then run it
```bash
docker run gau example.com
```
Bear in mind that piping command (echo "example.com" | gau) will not work with the docker container


## ohmyzsh note:
ohmyzsh's [git plugin](https://github.com/ohmyzsh/ohmyzsh/tree/master/plugins/git) has an alias which maps `gau` to the `git add --update` command. This is problematic, causing a binary conflict between this tool "gau" and the zsh plugin alias "gau" (`git add --update`). There is currently a few workarounds which can be found in this Github [issue](https://github.com/mr-pmillz/gau/issues/8). 


## Useful?

<a href="http://buymeacoff.ee/cdl" target="_blank"><img src="https://www.buymeacoffee.com/assets/img/custom_images/orange_img.png" alt="Buy Me A Coffee" style="height: 41px !important;width: 174px !important;box-shadow: 0px 3px 2px 0px rgba(190, 190, 190, 0.5) !important;-webkit-box-shadow: 0px 3px 2px 0px rgba(190, 190, 190, 0.5) !important;" ></a>

<a href="https://commoncrawl.org/donate/">Donate to CommonCrawl</a><br>
<a href="https://archive.org/donate">Donate to the InternetArchive</a>
