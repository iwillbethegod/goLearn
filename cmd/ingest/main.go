package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/ingest"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
	"github.com/ashishsinghbhadoria/goLearn/internal/repl"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

func main() {
	cfg := parseFlags()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	reg := processor.NewRegistry()
	reg.Register(processor.CSVProcessor{})

	proc, err := reg.Lookup(cfg.format)
	if err != nil {
		logger.Error("processor lookup failed", "format", cfg.format, "err", err)
		os.Exit(1)
	}

	files, err := ingest.Expand(cfg.paths, proc.Extensions())
	if err != nil {
		logger.Error("expand paths failed", "err", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		logger.Error("no input files matched", "format", cfg.format)
		os.Exit(1)
	}

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	store := user.NewStore()
	stats := &handler.Stats{}
	chain := handler.Chain(handler.Terminal,
		handler.WithPerWorkerCount(stats),
		handler.WithLogging(logger, cfg.verbose),
		handler.WithMetrics(stats),
		handler.WithCancelCheck(),
		handler.WithDedup(store),
		handler.WithProcess(makeMockProcessRow(cfg.workMin, cfg.workMax)),
	)

	p := pool.New(rootCtx, pool.WithQueueSize(cfg.queue))
	p.Start(cfg.workers)

	runner := ingest.NewRunner(proc, p, chain, stats, logger)

	scheduleAutoCancels(cfg.cancelList, cfg.cancelAfter, runner, logger)

	if cfg.repl {
		go repl.Run(rootCtx, repl.Controls{
			Pool:   p,
			Runner: runner,
			Stats:  stats,
			Store:  store,
			Cancel: rootCancel,
		})
	}

	logger.Info("ingest start",
		"workers", cfg.workers,
		"queue", cfg.queue,
		"files", len(files),
		"format", cfg.format,
		"verbose", cfg.verbose,
	)
	overall := time.Now()
	runner.Run(rootCtx, files)
	p.Stop()
	wall := time.Since(overall)

	printSummary(stats.Snapshot(), store, runner, wall, p.WorkerCount())
	rootCancel()
}

type config struct {
	workers     int
	queue       int
	format      string
	repl        bool
	cancelList  string
	cancelAfter time.Duration
	workMin     time.Duration
	workMax     time.Duration
	verbose     bool
	paths       []string
}

func parseFlags() config {
	workers := flag.Int("workers", 8, "initial worker count")
	queue := flag.Int("queue", 64, "buffered job channel capacity")
	format := flag.String("format", "csv", "processor name (csv)")
	enableRepl := flag.Bool("repl", true, "enable interactive REPL on stdin")
	cancelList := flag.String("cancel", "", "comma-separated file basenames to auto-cancel mid-flight")
	cancelAfter := flag.Duration("cancel-after", 30*time.Millisecond, "delay before auto-cancellation")
	workMin := flag.Duration("work-min", 10*time.Millisecond, "min mock-work duration per record")
	workMax := flag.Duration("work-max", 500*time.Millisecond, "max mock-work duration per record")
	verbose := flag.Bool("verbose", false, "log every record (high overhead at >100k rec/s)")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ingest [flags] <file-or-folder> [<file-or-folder> ...]")
		os.Exit(1)
	}
	if *workers < 1 {
		fmt.Fprintf(os.Stderr, "invalid -workers (must be >= 1): %d\n", *workers)
		os.Exit(1)
	}
	if *queue < 0 {
		fmt.Fprintf(os.Stderr, "invalid -queue (must be >= 0): %d\n", *queue)
		os.Exit(1)
	}

	return config{
		workers:     *workers,
		queue:       *queue,
		format:      *format,
		repl:        *enableRepl,
		cancelList:  *cancelList,
		cancelAfter: *cancelAfter,
		workMin:     *workMin,
		workMax:     *workMax,
		verbose:     *verbose,
		paths:       paths,
	}
}

func scheduleAutoCancels(list string, after time.Duration, runner *ingest.Runner, logger *slog.Logger) {
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		n := name
		time.AfterFunc(after, func() {
			if runner.CancelFile(n) {
				logger.Info("auto-cancel", "file", n, "after", after)
			}
		})
	}
}

func printSummary(snap handler.Snapshot, store *user.Store, runner *ingest.Runner, wall time.Duration, workers int) {
	fmt.Println()
	fmt.Println("=== summary ===")
	fmt.Printf("totals  ok=%d dedup=%d cancelled=%d parse_err=%d wall=%s\n",
		snap.OK, snap.Dedup, snap.Cancelled, snap.ParseErr, wall)
	fmt.Printf("stored=%d files=%d workers=%d\n", store.Count(), len(runner.Files()), workers)
	fmt.Printf("per-worker:")
	for _, wc := range snap.PerWorker {
		fmt.Printf(" w%d=%d", wc.ID, wc.Count)
	}
	fmt.Println()
	for _, fs := range runner.Files() {
		fmt.Printf("file=%s records=%d duration=%s\n", fs.Path, fs.Records, fs.Duration)
	}
}
