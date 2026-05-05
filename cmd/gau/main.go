package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/runner"
	"github.com/mr-pmillz/gau/v2/runner/flags"
	log "github.com/sirupsen/logrus"
)

func main() {
	cmd := flags.NewRootCmd(runGau)
	if err := cmd.Execute(); err != nil {
		// Cobra has already printed the error; we just propagate the exit
		// code. --help and --version are handled inside cobra and never
		// reach this branch.
		os.Exit(1)
	}
}

// runGau is the cobra RunE callback: it owns the actual fetch pipeline.
// Returning an error here makes cobra print it and Execute return non-nil.
func runGau(cfg *flags.Config, domains []string) error {
	pcfg, err := cfg.ProviderConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gau := new(runner.Runner)
	if err := gau.Init(ctx, pcfg, cfg.Providers, cfg.Filters); err != nil {
		log.Warn(err)
	}

	results := make(chan string)

	out := os.Stdout
	if pcfg.Output != "" {
		f, openErr := os.OpenFile(pcfg.Output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if openErr != nil {
			return fmt.Errorf("open output file: %w", openErr)
		}
		defer func() { _ = f.Close() }()
		out = f
	}

	writeOpts := output.WriteOptions{
		Blacklist:        pcfg.Blacklist,
		MatchExtensions:  pcfg.MatchExtensions,
		MatchRegex:       pcfg.MatchRegex,
		RemoveParameters: pcfg.RemoveParameters,
		DedupCap:         pcfg.FPCap,
	}

	writeErr := make(chan error, 1)
	var writeWg sync.WaitGroup
	writeWg.Add(1)
	go func(out io.Writer, useJSON bool) {
		defer writeWg.Done()
		if useJSON {
			output.WriteURLsJSON(out, results, writeOpts)
			writeErr <- nil
			return
		}
		writeErr <- output.WriteURLs(out, results, writeOpts)
	}(out, pcfg.JSON)

	workChan := make(chan runner.Work)
	gau.Start(ctx, workChan, results)

	if len(domains) > 0 {
		for _, provider := range gau.Providers {
			for _, domain := range domains {
				workChan <- runner.NewWork(domain, provider)
			}
		}
	} else {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			domain := sc.Text()
			for _, provider := range gau.Providers {
				workChan <- runner.NewWork(domain, provider)
			}
		}
		if scErr := sc.Err(); scErr != nil {
			close(workChan)
			gau.Wait()
			close(results)
			writeWg.Wait()
			return fmt.Errorf("read stdin: %w", scErr)
		}
	}
	close(workChan)

	gau.Wait()
	close(results)
	writeWg.Wait()

	if err := <-writeErr; err != nil {
		return fmt.Errorf("write results: %w", err)
	}
	return nil
}
