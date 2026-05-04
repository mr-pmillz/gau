# CLAUDE.md

Guidance for Claude Code working in this repository.

## Project

`gau` (getallurls) is a Go CLI that fetches known URLs for a given domain from four passive sources: the Wayback Machine, Common Crawl, AlienVault OTX, and URLScan. Output goes to stdout (or a file) as plain text or JSON.

This repo is a fork of `github.com/lc/gau`. The upstream maintainer is unresponsive to issues and the design has caused real downstream problems — Common Crawl's maintainer added strict rate-limits in part because of how this tool hits their API. The fork's purpose is to fix those problems without coordinating with upstream. Do **not** open PRs against `lc/gau` and do **not** add upstream as a remote. The module path is `github.com/mr-pmillz/gau/v2` and all internal imports must use that path.

## Repo Layout

```
cmd/gau/main.go                 — entry point: wires flags → runner → writer
runner/
  runner.go                     — worker pool, dispatches Work over a channel
  flags/flags.go                — pflag + viper config loading, .gau.toml
pkg/providers/
  providers.go                  — Provider interface, shared Config, Version
  filters.go                    — status/mime/date filters → URL params
  wayback/                      — web.archive.org CDX API
  commoncrawl/                  — index.commoncrawl.org (fetches collinfo.json on init)
  otx/                          — otx.alienvault.com
  urlscan/                      — urlscan.io
pkg/httpclient/client.go        — fasthttp wrapper, retry loop, random UA
pkg/output/output.go            — channel→writer, blacklist + dedup
.gau.toml                       — example config (TOML, mapstructure tags)
```

All four providers implement `providers.Provider` (`Fetch(ctx, domain, results) error` + `Name()`). The runner spawns `Threads` workers that pull `Work{domain, provider}` items and stream results into a single `chan string`. Output runs in its own goroutine; closing the results channel ends the program.

## Build / Run / Test

```bash
go build -o gau ./cmd/gau           # local build
go install ./cmd/gau                # install to $GOBIN
go test -race ./...                 # tests with race detector (no tests yet — add some)
go vet ./...
golangci-lint run                   # available at /home/phil/go/bin/golangci-lint
go mod tidy                         # after dep changes
```

Quick smoke test:
```bash
printf example.com | ./gau --threads 2 --providers wayback --verbose
```

## Module Path & Forking Hygiene

- Module: `github.com/mr-pmillz/gau/v2` (see `go.mod`).
- Every internal import already uses this path. If you add a new package, import it as `github.com/mr-pmillz/gau/v2/...` — never `github.com/lc/gau/...`.
- The Docker image tag in `.github/workflows/cicd-to-dockerhub.yml` and the install instructions in `README.md` reference this fork. Keep them aligned.
- `go.mod` declares `go 1.26.2`. The release workflow pins Go 1.23.2 and the Dockerfile pins Go 1.21.0 — these are out of sync and will need updating before the next release.

## Known Problem Areas (from upstream issue history)

These are why the fork exists. Treat them as live concerns when touching the relevant code:

- **Common Crawl hammering** (`pkg/providers/commoncrawl/commoncrawl.go`). No per-request rate limiting; retries are unconditional in `httpclient.MakeRequest`; pagination loop has no backoff. Common Crawl now rate-limits aggressively because of this pattern.
- **Wayback pagination** (`pkg/providers/wayback/wayback.go`). The `for page := 0; ; page++` loop only exits via `ErrBadRequest` or empty result — but `break` inside the `select` only breaks the select, not the outer loop, so an empty page does **not** terminate. This bug ships today.
- **httpclient retry/backoff** (`pkg/httpclient/client.go`). Tight retry loop, no jitter, no backoff, `math/rand` seeded only by package init. Errors other than `ErrBadRequest` are retried `MaxRetries` times with no delay.
- **TLS verification disabled** (`runner/flags/flags.go`: `InsecureSkipVerify: true`). Hardcoded; no flag to enforce verification.
- **No context propagation into HTTP calls.** `httpclient.MakeRequest` ignores `ctx`, so cancelling the runner doesn't cancel in-flight HTTP — workers can hang on `DoTimeout`.
- **Blacklist + `--fp`** (`pkg/output/output.go`, recently fixed in 56bb83f). The previous bug inverted the blacklist match and compared extensions with vs without leading dots. New filtering bugs in this area should be assumed to be regressions of that class.
- **`--fp` dedup uses an unbounded set** (`output.WriteURLs`). Memory grows without limit on large crawls.
- **Retracted versions in `go.mod`**: v2.0.1, v2.0.2, v2.0.3, v2.0.7. Don't reuse those tags.

When fixing any of these, prefer minimal, focused diffs — the goal is shipping fixes the upstream wouldn't merge, not rewriting the tool.

## Conventions

- Wrap errors with `fmt.Errorf("...: %w", err)`. Several spots use `%s` or `%v` on errors — that's a bug-shaped pattern, fix when you touch them.
- Provider clients verify interface compliance via `var _ providers.Provider = (*Client)(nil)`. Keep that pattern in any new provider.
- `runner/flags/flags.go` is where new CLI flags get added; flags are bound through `pflag` → `viper` → `Config` (mapstructure tag), then copied into `providers.Config` in `ProviderConfig()`. Adding a flag means changes in all three places + the `.gau.toml` example.
- Logging is `logrus` with `WithFields({"provider": Name, "page": n})`. Keep that field shape so log filtering stays consistent.

## Commits / PRs

- `git commit --no-verify` — the pre-commit hook fires `golangci-lint` in a way that fails under parallel invocation. Run `golangci-lint run` manually instead.
- Conventional commit prefixes (`fix:`, `feat:`, `refactor:`, `ci:`).
- This is a hostile fork — PRs go to `mr-pmillz/gau`, never `lc/gau`.
