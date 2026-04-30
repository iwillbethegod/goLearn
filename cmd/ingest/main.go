package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/csvr"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

func main() {
	workers := flag.Int("workers", 8, "worker count")
	queue := flag.Int("queue", 64, "buffered job channel size")
	cancelList := flag.String("cancel", "", "comma-separated CSV basenames to cancel mid-flight")
	cancelAfter := flag.Duration("cancel-after", 30*time.Millisecond, "delay before cancelling the listed files")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		log.Fatal("usage: ingest [flags] file1.csv file2.csv ...")
	}

	cancelTargets := map[string]struct{}{}
	for _, name := range strings.Split(*cancelList, ",") {
		if name = strings.TrimSpace(name); name != "" {
			cancelTargets[name] = struct{}{}
		}
	}

	store := user.NewStore()
	p := pool.New(*workers, *queue, store)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	p.Start(rootCtx)

	overall := time.Now()
	var fwg sync.WaitGroup
	for _, path := range files {
		path := path
		fwg.Add(1)
		go func() {
			defer fwg.Done()
			processFile(rootCtx, p, path, cancelTargets, *cancelAfter)
		}()
	}
	fwg.Wait()

	p.Stop()
	log.Printf("[main] done: stored=%d total=%s", store.Count(), time.Since(overall))
}

func processFile(rootCtx context.Context, p *pool.Pool, path string, cancelTargets map[string]struct{}, delay time.Duration) {
	fctx, fcancel := context.WithCancel(rootCtx)
	defer fcancel()

	if _, ok := cancelTargets[filepath.Base(path)]; ok {
		time.AfterFunc(delay, func() {
			log.Printf("[main] CANCEL  file=%s after=%s", path, delay)
			fcancel()
		})
	}

	start := time.Now()
	stream, err := csvr.Stream(fctx, path)
	if err != nil {
		log.Printf("[main] open failed file=%s err=%v", path, err)
		return
	}

	var jobs sync.WaitGroup
	streamed := 0
	for rec := range stream {
		if rec.Err != nil {
			log.Printf("[main] csv error file=%s err=%v", path, rec.Err)
			continue
		}
		jobs.Add(1)
		p.Submit(pool.Job{
			File: filepath.Base(path),
			User: rec.User,
			Ctx:  fctx,
			Done: jobs.Done,
		})
		streamed++
	}
	jobs.Wait()
	log.Printf("[main] FILE    file=%s streamed=%d elapsed=%s", path, streamed, time.Since(start))
}
