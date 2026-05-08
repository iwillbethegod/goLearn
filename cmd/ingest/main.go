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
	"text/tabwriter"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/ingest"
	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/observability"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
	"github.com/ashishsinghbhadoria/goLearn/internal/repl"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	pkglogger "github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := parseFlags()
	logger := slog.New(pkglogger.NewTraceHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
	))

	otelShutdown, err := observability.Init(context.Background(), observability.Config{
		ServiceName: firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), "goLearn-ingest"),
		Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Exporter:    os.Getenv("OTEL_TRACES_EXPORTER"),
	})
	if err != nil {
		return fmt.Errorf("observability init: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("otel shutdown", "err", err)
		}
	}()

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.RepositoryType(cfg.storage),
		JSONPath: cfg.storePath,
		DSN:      cfg.dbDSN,
		Ctx:      context.Background(),
		Logger:   logger,
	})
	if err != nil {
		return fmt.Errorf("init repository: %w", err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			logger.Error("repository close", "err", err)
		}
	}()
	svc := user.NewService(repo, logger, metrics.New())

	switch {
	case cfg.register:
		return runRegister(svc, cfg)
	case cfg.list:
		return runList(svc)
	case cfg.deleteProfile:
		return runDeleteProfile(svc, cfg)
	default:
		return runIngest(cfg, logger, svc)
	}
}

// runList prints all users in the persistent store as a tab-aligned
// table. This is the Day-1 "List users" deliverable surface — no
// auth, no gRPC, just the repository read-through.
func runList(svc *user.Service) error {
	users, total, err := svc.ListUsers(context.Background(), 0, 0)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if total == 0 {
		fmt.Println("(no users registered)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tEMAIL")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%s\t%s\n", u.ID, u.Name, u.Email)
	}
	return w.Flush()
}

// runRegister handles `-register` and exits the process. Auth is not
// required (registering IS the way to get auth).
func runRegister(svc *user.Service, cfg config) error {
	if cfg.email == "" || cfg.password == "" || cfg.name == "" {
		return errors.New("usage: ingest -register -email <e> -name <n> -password <p>")
	}
	u, err := svc.Register(context.Background(), cfg.name, cfg.email, cfg.password)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	fmt.Printf("registered user %s <%s> (id=%s)\n", u.Name, u.Email, u.ID)
	return nil
}

// runDeleteProfile authenticates the caller and then deletes their
// own profile from the persistent store.
func runDeleteProfile(svc *user.Service, cfg config) error {
	if cfg.email == "" || cfg.password == "" {
		return errors.New("usage: ingest -delete-profile -email <e> -password <p>")
	}
	if err := svc.DeleteByEmail(context.Background(), cfg.email, cfg.password); err != nil {
		if errors.Is(err, model.ErrInvalidCredential) {
			return errors.New("delete failed: invalid email or password")
		}
		return fmt.Errorf("delete: %w", err)
	}
	fmt.Printf("profile deleted for %s\n", cfg.email)
	return nil
}

// runIngest authenticates and then runs the concurrent CSV pipeline.
func runIngest(cfg config, logger *slog.Logger, svc *user.Service) error {
	if cfg.email == "" || cfg.password == "" {
		return errors.New("ingest requires authentication: -email <e> -password <p> (or use -register / -delete-profile)")
	}
	if len(cfg.paths) == 0 {
		return errors.New("usage: ingest [flags] <file-or-folder> [<file-or-folder> ...]")
	}

	authedUser, err := svc.Login(context.Background(), cfg.email, cfg.password)
	if err != nil {
		if errors.Is(err, model.ErrInvalidCredential) {
			return errors.New("login failed: invalid email or password")
		}
		return fmt.Errorf("login: %w", err)
	}

	reg := processor.NewRegistry()
	reg.Register(processor.CSVProcessor{})

	proc, err := reg.Lookup(cfg.format)
	if err != nil {
		return fmt.Errorf("processor lookup format=%s: %w", cfg.format, err)
	}

	files, err := ingest.Expand(cfg.paths, proc.Extensions())
	if err != nil {
		return fmt.Errorf("expand paths: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no input files matched format=%s", cfg.format)
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

	// Optional: gRPC token gate. If -grpc-addr is empty, ingest runs
	// without rate limiting (legacy day-3 behavior).
	if cfg.grpcAddr != "" {
		conn, err := grpc.NewClient(cfg.grpcAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return fmt.Errorf("grpc dial %s: %w", cfg.grpcAddr, err)
		}
		defer conn.Close()
		gate := newTokenGate(userpb.NewUserServiceClient(conn), authedUser.ID, logger)
		runner.WithGate(gate)
		logger.Info("token gate enabled", "addr", cfg.grpcAddr, "user_id", authedUser.ID)
	}

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
	return nil
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
	list          bool
	deleteProfile bool
	email         string
	password      string
	name          string
	storePath     string
	storage       string
	dbDSN         string

	grpcAddr string

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
	list := flag.Bool("list", false, "print all users in the persistent store and exit (no auth)")
	deleteProfile := flag.Bool("delete-profile", false, "authenticate (with -email -password) and delete the user, then exit")
	email := flag.String("email", "", "user email (login or register)")
	password := flag.String("password", "", "user password (or set $INGEST_PASSWORD)")
	name := flag.String("name", "", "user name (register only)")
	storePath := flag.String("store-path", ".data/users.json", "path to the persistent user store (jsonfile)")
	storage := flag.String("storage", "jsonfile", "storage strategy: memory | jsonfile | postgres")
	dbDSN := flag.String("db-dsn", os.Getenv("DATABASE_URL"), "Postgres DSN; defaults to $DATABASE_URL (postgres only)")
	grpcAddr := flag.String("grpc-addr", "", "gRPC user-service address (e.g. :9090). Empty disables the token gate.")

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
		list:          *list,
		deleteProfile: *deleteProfile,
		email:         *email,
		password:      pwd,
		name:          *name,
		storePath:     *storePath,
		storage:       *storage,
		dbDSN:         *dbDSN,
		grpcAddr:      *grpcAddr,
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
