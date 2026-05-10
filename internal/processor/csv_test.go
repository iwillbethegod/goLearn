package processor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
)

// writeCSV is a helper that drops a CSV file in a tempdir and returns
// the path. Each test gets its own dir so concurrent runs don't fight.
func writeCSV(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "users.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return p
}

func collectAll(t *testing.T, ch <-chan processor.Record) []processor.Record {
	t.Helper()
	var out []processor.Record
	timeout := time.After(2 * time.Second)
	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, r)
		case <-timeout:
			t.Fatal("Stream did not close within 2s")
		}
	}
}

func TestCSV_NameAndExtensions(t *testing.T) {
	p := processor.CSVProcessor{}
	if p.Name() != "csv" {
		t.Fatalf("Name = %q, want csv", p.Name())
	}
	exts := p.Extensions()
	if len(exts) != 1 || exts[0] != ".csv" {
		t.Fatalf("Extensions = %v, want [.csv]", exts)
	}
}

func TestCSV_HappyPath(t *testing.T) {
	path := writeCSV(t, "id,name,email\nu-1,Ada,ada@x.com\nu-2,Bob,bob@x.com\n")
	ch, err := processor.CSVProcessor{}.Stream(context.Background(), path)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := collectAll(t, ch)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.Err != nil {
			t.Fatalf("unexpected err: %v", r.Err)
		}
	}
	if got[0].User.ID != "u-1" || got[1].User.Email != "bob@x.com" {
		t.Fatalf("rows = %+v", got)
	}
}

func TestCSV_StripsUTF8BOM(t *testing.T) {
	body := "\xef\xbb\xbfid,name,email\nu-1,Ada,ada@x.com\n"
	path := writeCSV(t, body)
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("BOM-prefixed file should parse cleanly: %+v", got)
	}
	if got[0].User.ID != "u-1" {
		t.Fatalf("ID = %q (BOM may have leaked into first field)", got[0].User.ID)
	}
}

func TestCSV_HeaderCaseInsensitive(t *testing.T) {
	path := writeCSV(t, "ID,Name,EMAIL\nu-1,Ada,ada@x.com\n")
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("uppercase header should validate: %+v", got)
	}
}

func TestCSV_HeaderColumnSwap(t *testing.T) {
	path := writeCSV(t, "name,id,email\nAda,u-1,ada@x.com\n")
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	if len(got) != 1 || got[0].Err == nil {
		t.Fatalf("swapped header MUST surface as Record.Err: %+v", got)
	}
}

func TestCSV_WrongColumnCount(t *testing.T) {
	path := writeCSV(t, "id,name\nu-1,Ada\n")
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	if len(got) != 1 || got[0].Err == nil {
		t.Fatalf("2-column header should error: %+v", got)
	}
}

func TestCSV_EmptyFile(t *testing.T) {
	path := writeCSV(t, "")
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	if len(got) != 0 {
		t.Fatalf("empty file should yield no rows; got %+v", got)
	}
}

func TestCSV_ParseErrorContinues(t *testing.T) {
	// First data row is broken (4 columns), second row is valid. Expect
	// one Err record + one good record.
	body := "id,name,email\nu-1,Ada,ada@x.com,extra\nu-2,Bob,bob@x.com\n"
	path := writeCSV(t, body)
	ch, _ := processor.CSVProcessor{}.Stream(context.Background(), path)
	got := collectAll(t, ch)
	var errs, oks int
	for _, r := range got {
		if r.Err != nil {
			errs++
		} else {
			oks++
		}
	}
	if errs == 0 {
		t.Fatalf("expected at least one parse error, got %+v", got)
	}
}

func TestCSV_ContextCancelStops(t *testing.T) {
	body := "id,name,email\n"
	for i := 0; i < 1000; i++ {
		body += "u,a,a@x\n"
	}
	path := writeCSV(t, body)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before reading even starts
	ch, _ := processor.CSVProcessor{}.Stream(ctx, path)
	// Channel must close promptly without producing all 1000 rows.
	done := make(chan struct{})
	go func() {
		count := 0
		for range ch {
			count++
			if count > 100 {
				t.Errorf("read %d rows after cancel", count)
				return
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close on cancel")
	}
}

func TestCSV_OpenError(t *testing.T) {
	_, err := processor.CSVProcessor{}.Stream(context.Background(), "/nonexistent/path.csv")
	if err == nil {
		t.Fatal("expected open error for missing path")
	}
}

func TestRegistry_RegisterLookup(t *testing.T) {
	r := processor.NewRegistry()
	r.Register(processor.CSVProcessor{})

	p, err := r.Lookup("csv")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.Name() != "csv" {
		t.Fatalf("processor name = %q, want csv", p.Name())
	}
	if _, err := r.Lookup("json"); err == nil {
		t.Fatal("Lookup unknown should error")
	}
}

func TestRegistry_ForExt(t *testing.T) {
	r := processor.NewRegistry()
	r.Register(processor.CSVProcessor{})

	if p, ok := r.ForExt("data/users.csv"); !ok || p.Name() != "csv" {
		t.Fatalf("ForExt(.csv) = %v %v, want csv true", p, ok)
	}
	if p, ok := r.ForExt("data/users.CSV"); !ok || p.Name() != "csv" {
		t.Fatalf("ForExt mixed-case ext should match: %v %v", p, ok)
	}
	if _, ok := r.ForExt("data/users.json"); ok {
		t.Fatalf("ForExt(.json) should miss")
	}
}
