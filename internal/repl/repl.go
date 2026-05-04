// Package repl provides a stdin command interface for inspecting and
// resizing a running ingest job. The package depends only on small
// interfaces (Pool, Runner, Stats, Counter) so it stays decoupled from
// the concrete pool / runner / store types.
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
)

// Pool is the subset of pool.Pool the REPL uses.
type Pool interface {
	AddWorker() int
	RemoveWorker() (int, error)
	WorkerCount() int
	QueueLen() int
}

// Runner is the subset of ingest.Runner the REPL uses.
type Runner interface {
	CancelFile(basename string) bool
	ActiveFiles() []string
}

// Stats reads pipeline stats via Snapshot.
type Stats interface {
	Snapshot() handler.Snapshot
}

// Counter exposes a count-only readout (e.g. user.DedupStore.Count).
type Counter interface {
	Count() int
}

// Controls bundles the dependencies the REPL operates on.
type Controls struct {
	Pool   Pool
	Runner Runner
	Stats  Stats
	Store  Counter
	Cancel context.CancelFunc

	// In and Out default to os.Stdin / os.Stdout. Tests can override.
	In  io.Reader
	Out io.Writer
}

// Run reads commands from c.In until ctx is done or input closes.
func Run(ctx context.Context, c Controls) {
	if c.In == nil {
		c.In = os.Stdin
	}
	if c.Out == nil {
		c.Out = os.Stdout
	}

	fmt.Fprintln(c.Out, "repl: add [N] | remove [N] | status | files | cancel <name> | quit")

	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(c.In)
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
			handleCommand(strings.TrimSpace(line), c)
		}
	}
}

func handleCommand(line string, c Controls) {
	if line == "" {
		return
	}
	parts := strings.Fields(line)
	switch parts[0] {
	case "add":
		cmdAdd(c, parts)
	case "remove":
		cmdRemove(c, parts)
	case "status":
		cmdStatus(c)
	case "files":
		cmdFiles(c)
	case "cancel":
		cmdCancel(c, parts)
	case "quit", "exit":
		c.Cancel()
	default:
		fmt.Fprintf(c.Out, "unknown command: %s\n", parts[0])
	}
}

func cmdAdd(c Controls, parts []string) {
	for i := 0; i < parseN(parts, 1); i++ {
		fmt.Fprintf(c.Out, "+ worker %d\n", c.Pool.AddWorker())
	}
}

func cmdRemove(c Controls, parts []string) {
	for i := 0; i < parseN(parts, 1); i++ {
		id, err := c.Pool.RemoveWorker()
		if err != nil {
			fmt.Fprintln(c.Out, err)
			return
		}
		fmt.Fprintf(c.Out, "- worker %d\n", id)
	}
}

func cmdStatus(c Controls) {
	snap := c.Stats.Snapshot()
	fmt.Fprintf(c.Out, "workers=%d queued=%d ok=%d dedup=%d cancelled=%d parse_err=%d stored=%d\n",
		c.Pool.WorkerCount(), c.Pool.QueueLen(),
		snap.OK, snap.Dedup, snap.Cancelled, snap.ParseErr,
		c.Store.Count(),
	)
	var b strings.Builder
	for _, wc := range snap.PerWorker {
		fmt.Fprintf(&b, "w%d=%d ", wc.ID, wc.Count)
	}
	if b.Len() > 0 {
		fmt.Fprintln(c.Out, " ", strings.TrimSpace(b.String()))
	}
}

func cmdFiles(c Controls) {
	active := c.Runner.ActiveFiles()
	if len(active) == 0 {
		fmt.Fprintln(c.Out, "(no active files)")
		return
	}
	fmt.Fprintln(c.Out, strings.Join(active, ", "))
}

func cmdCancel(c Controls, parts []string) {
	if len(parts) < 2 {
		fmt.Fprintln(c.Out, "usage: cancel <basename>")
		return
	}
	if c.Runner.CancelFile(parts[1]) {
		fmt.Fprintf(c.Out, "cancelled file=%s\n", parts[1])
		return
	}
	fmt.Fprintf(c.Out, "not active: %s\n", parts[1])
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
