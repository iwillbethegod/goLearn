package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/ingest"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

func main() {
	workers := flag.Int("workers", 8, "initial worker count")
	queue := flag.Int("queue", 64, "buffered job channel capacity")
	format := flag.String("format", "csv", "processor name (csv)")
	repl := flag.Bool("repl", true, "enable interactive REPL on stdin")
	cancelList := flag.String("cancel", "", "comma-separated file basenames to auto-cancel mid-flight")
	cancelAfter := flag.Duration("cancel-after", 30*time.Millisecond, "delay before auto-cancellation")
	workMin := flag.Duration("work-min", 10*time.Millisecond, "min mock-work duration per record")
	workMax := flag.Duration("work-max", 500*time.Millisecond, "max mock-work duration per record")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		log.Fatal("usage: ingest [flags] <file-or-folder> [<file-or-folder> ...]")
	}

	reg := processor.NewRegistry()
	reg.Register(processor.CSVProcessor{})

	proc, err := reg.Lookup(*format)
	if err != nil {
		log.Fatal(err)
	}

	files, err := ingest.Expand(paths, proc.Extensions())
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatal("no input files matched")
	}

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	store := user.NewStore()
	p := pool.New(rootCtx, *queue, store, pool.MakeMockProcessRow(*workMin, *workMax))
	p.Start(*workers)

	runner := ingest.NewRunner(proc, p)

	for _, name := range strings.Split(*cancelList, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		n := name
		time.AfterFunc(*cancelAfter, func() {
			if runner.CancelFile(n) {
				log.Printf("auto-cancel file=%s after=%s", n, *cancelAfter)
			}
		})
	}

	if *repl {
		go runREPL(rootCtx, rootCancel, p, runner, store)
	}

	log.Printf("ingest start workers=%d queue=%d files=%d format=%s", *workers, *queue, len(files), *format)
	overall := time.Now()
	runner.Run(rootCtx, files)
	p.Stop()
	wall := time.Since(overall)

	printSummary(p, store, runner, wall)
	rootCancel()
}

func printSummary(p *pool.Pool, store *user.Store, runner *ingest.Runner, wall time.Duration) {
	fmt.Println()
	fmt.Println("=== summary ===")
	fmt.Printf("totals  ok=%d dedup=%d cancelled=%d parse_err=%d wall=%s\n",
		p.Stats.OK.Load(), p.Stats.Dedup.Load(), p.Stats.Cancelled.Load(), p.Stats.ParseErr.Load(), wall)
	fmt.Printf("stored=%d files=%d workers=%d\n", store.Count(), len(runner.Files()), p.WorkerCount())
	fmt.Printf("per-worker:")
	for _, wc := range p.Stats.PerWorker() {
		fmt.Printf(" w%d=%d", wc.ID, wc.Count)
	}
	fmt.Println()
	for _, fs := range runner.Files() {
		fmt.Printf("file=%s records=%d duration=%s\n", fs.Path, fs.Records, fs.Duration)
	}
}

// runREPL reads commands on stdin while ingestion runs. The stdin Scan
// blocks; we wrap it in a goroutine so the outer select can also watch
// rootCtx and exit when ingestion completes.
func runREPL(ctx context.Context, cancel context.CancelFunc, p *pool.Pool, r *ingest.Runner, s *user.Store) {
	fmt.Println("repl: add [N] | remove [N] | status | files | cancel <name> | quit")
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			handleCommand(strings.TrimSpace(line), p, r, s, cancel)
		}
	}
}

func handleCommand(line string, p *pool.Pool, r *ingest.Runner, s *user.Store, cancel context.CancelFunc) {
	if line == "" {
		return
	}
	parts := strings.Fields(line)
	switch parts[0] {
	case "add":
		cmdAdd(p, parts)
	case "remove":
		cmdRemove(p, parts)
	case "status":
		cmdStatus(p, s)
	case "files":
		cmdFiles(r)
	case "cancel":
		cmdCancel(r, parts)
	case "quit", "exit":
		cancel()
	default:
		fmt.Printf("unknown command: %s\n", parts[0])
	}
}

func cmdAdd(p *pool.Pool, parts []string) {
	for i := 0; i < parseN(parts, 1); i++ {
		fmt.Printf("+ worker %d\n", p.AddWorker())
	}
}

func cmdRemove(p *pool.Pool, parts []string) {
	for i := 0; i < parseN(parts, 1); i++ {
		id, err := p.RemoveWorker()
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("- worker %d\n", id)
	}
}

func cmdStatus(p *pool.Pool, s *user.Store) {
	fmt.Printf("workers=%d queued=%d ok=%d dedup=%d cancelled=%d parse_err=%d stored=%d\n",
		p.WorkerCount(), p.QueueLen(),
		p.Stats.OK.Load(), p.Stats.Dedup.Load(), p.Stats.Cancelled.Load(), p.Stats.ParseErr.Load(),
		s.Count())
	var b strings.Builder
	for _, wc := range p.Stats.PerWorker() {
		fmt.Fprintf(&b, "w%d=%d ", wc.ID, wc.Count)
	}
	if b.Len() > 0 {
		fmt.Println(" ", strings.TrimSpace(b.String()))
	}
}

func cmdFiles(r *ingest.Runner) {
	active := r.ActiveFiles()
	if len(active) == 0 {
		fmt.Println("(no active files)")
		return
	}
	fmt.Println(strings.Join(active, ", "))
}

func cmdCancel(r *ingest.Runner, parts []string) {
	if len(parts) < 2 {
		fmt.Println("usage: cancel <basename>")
		return
	}
	if r.CancelFile(parts[1]) {
		fmt.Printf("cancelled file=%s\n", parts[1])
		return
	}
	fmt.Printf("not active: %s\n", parts[1])
}

func parseN(parts []string, def int) int {
	if len(parts) < 2 {
		return def
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 {
		return def
	}
	return n
}
