package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/ingest"
	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
	"github.com/ashishsinghbhadoria/goLearn/internal/repl"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	cfg := parseFlags()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.RepositoryType(cfg.storage),
		JSONPath: cfg.storePath,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("init repository failed", "err", err)
		os.Exit(1)
	}
	svc := user.NewService(repo, logger, metrics.New())

	switch {
	case cfg.register:
		runRegister(svc, cfg)
	case cfg.deleteProfile:
		runDeleteProfile(svc, cfg)
	default:
		runIngest(cfg, logger, svc)
	}
}

// runRegister handles `-register` and exits the process. Auth is not
// required (registering IS the way to get auth). Successful exit 0,
// any failure exit 1.
func runRegister(svc *user.Service, cfg config) {
	if cfg.email == "" || cfg.password == "" || cfg.name == "" {
		fmt.Fprintln(os.Stderr, "usage: ingest -register -email <e> -name <n> -password <p>")
		os.Exit(1)
	}
	u, err := svc.Register(context.Background(), cfg.name, cfg.email, cfg.password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "register failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("registered user %s <%s> (id=%s)\n", u.Name, u.Email, u.ID)
}

// runDeleteProfile authenticates the caller and then deletes their
// own profile from the persistent store.
func runDeleteProfile(svc *user.Service, cfg config) {
	if cfg.email == "" || cfg.password == "" {
		fmt.Fprintln(os.Stderr, "usage: ingest -delete-profile -email <e> -password <p>")
		os.Exit(1)
	}
	if err := svc.DeleteByEmail(context.Background(), cfg.email, cfg.password); err != nil {
		if errors.Is(err, model.ErrInvalidCredential) {
			fmt.Fprintln(os.Stderr, "delete failed: invalid email or password")
		} else {
			fmt.Fprintf(os.Stderr, "delete failed: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Printf("profile deleted for %s\n", cfg.email)
}

// runIngest authenticates and then runs the concurrent CSV pipeline.
func runIngest(cfg config, logger *slog.Logger, svc *user.Service) {
	if cfg.email == "" || cfg.password == "" {
		fmt.Fprintln(os.Stderr, "ingest requires authentication: -email <e> -password <p> (or use -register / -delete-profile)")
		os.Exit(1)
	}
	if len(cfg.paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ingest [flags] <file-or-folder> [<file-or-folder> ...]")
		os.Exit(1)
	}

	authedUser, err := svc.Login(context.Background(), cfg.email, cfg.password)
	if err != nil {
		if errors.Is(err, model.ErrInvalidCredential) {
			fmt.Fprintln(os.Stderr, "login failed: invalid email or password")
		} else {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		}
		os.Exit(1)
	}

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

	dedupStore := user.NewDedupStore()
	stats := &handler.Stats{}
	chain := handler.Chain(handler.Terminal,
		handler.WithPerWorkerCount(stats),
		handler.WithLogging(logger, cfg.verbose),
		handler.WithMetrics(stats),
		handler.WithCancelCheck(),
		handler.WithDedup(dedupStore),
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
			Store:  dedupStore,
			Cancel: rootCancel,
		})
	}

	logger.Info("ingest start",
		"user", authedUser.Email,
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

	printSummary(stats.Snapshot(), dedupStore, runner, wall, p.WorkerCount(), authedUser.Email)
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

	register      bool
	deleteProfile bool
	email         string
	password      string
	name          string
	storePath     string
	storage       string

	paths []string
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

	register := flag.Bool("register", false, "register a new user (with -email -name -password) and exit")
	deleteProfile := flag.Bool("delete-profile", false, "authenticate (with -email -password) and delete the user, then exit")
	email := flag.String("email", "", "user email (login or register)")
	password := flag.String("password", "", "user password (or set $INGEST_PASSWORD)")
	name := flag.String("name", "", "user name (register only)")
	storePath := flag.String("store-path", ".data/users.json", "path to the persistent user store (jsonfile)")
	storage := flag.String("storage", "jsonfile", "storage strategy: memory or jsonfile")

	flag.Parse()

	if *workers < 1 {
		fmt.Fprintf(os.Stderr, "invalid -workers (must be >= 1): %d\n", *workers)
		os.Exit(1)
	}
	if *queue < 0 {
		fmt.Fprintf(os.Stderr, "invalid -queue (must be >= 0): %d\n", *queue)
		os.Exit(1)
	}

	pwd := *password
	if pwd == "" {
		pwd = os.Getenv("INGEST_PASSWORD")
	}

	return config{
		workers:       *workers,
		queue:         *queue,
		format:        *format,
		repl:          *enableRepl,
		cancelList:    *cancelList,
		cancelAfter:   *cancelAfter,
		workMin:       *workMin,
		workMax:       *workMax,
		verbose:       *verbose,
		register:      *register,
		deleteProfile: *deleteProfile,
		email:         *email,
		password:      pwd,
		name:          *name,
		storePath:     *storePath,
		storage:       *storage,
		paths:         flag.Args(),
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

func printSummary(snap handler.Snapshot, store *user.DedupStore, runner *ingest.Runner, wall time.Duration, workers int, authedEmail string) {
	fmt.Println()
	fmt.Println("=== summary ===")
	fmt.Printf("user=%s\n", authedEmail)
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
