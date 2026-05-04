package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"sync"

	"github.com/mr-pmillz/gau/v2/pkg/output"
	"github.com/mr-pmillz/gau/v2/runner"
	"github.com/mr-pmillz/gau/v2/runner/flags"
	log "github.com/sirupsen/logrus"
)

func main() {
	cfg, err := flags.New().ReadInConfig()
	if err != nil {
		log.Warnf("error reading config: %v", err)
	}

	config, err := cfg.ProviderConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gau := new(runner.Runner)
	if err = gau.Init(ctx, config, cfg.Providers, cfg.Filters); err != nil {
		log.Warn(err)
	}

	results := make(chan string)

	out := os.Stdout
	if config.Output != "" {
		out, err = os.OpenFile(config.Output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatalf("Could not open output file: %v\n", err)
		}
		defer func() { _ = out.Close() }()
	}

	var writeWg sync.WaitGroup
	writeWg.Add(1)
	go func(out io.Writer, useJSON bool) {
		defer writeWg.Done()
		if useJSON {
			output.WriteURLsJSON(out, results, config.Blacklist, config.RemoveParameters, config.FPCap)
			return
		}
		if werr := output.WriteURLs(out, results, config.Blacklist, config.RemoveParameters, config.FPCap); werr != nil {
			log.Fatalf("error writing results: %v\n", werr)
		}
	}(out, config.JSON)

	workChan := make(chan runner.Work)
	gau.Start(ctx, workChan, results)

	domains := flags.Args()
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
		if err := sc.Err(); err != nil {
			log.Fatal(err)
		}
	}
	close(workChan)

	gau.Wait()
	close(results)
	writeWg.Wait()
}
