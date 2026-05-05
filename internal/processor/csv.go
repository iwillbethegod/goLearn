package processor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
)

// CSVProcessor reads files with header `id,name,email`. Header column
// names are validated case-insensitively so a swap (e.g. `name,id,email`)
// surfaces as a parse error rather than silent data corruption.
type CSVProcessor struct{}

func (CSVProcessor) Name() string         { return "csv" }
func (CSVProcessor) Extensions() []string { return []string{".csv"} }

// utf8BOM is the byte sequence (EF BB BF) written by some Windows
// tools at the start of UTF-8 files. Left in place it leaks into the
// first parsed field as a U+FEFF prefix. We strip it before handing
// the reader to encoding/csv.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

var expectedHeader = []string{"id", "name", "email"}

func (CSVProcessor) Stream(ctx context.Context, path string) (<-chan Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	out := make(chan Record)
	go func() {
		defer close(out)
		defer f.Close()
		streamRows(ctx, f, out)
	}()
	return out, nil
}

// streamRows owns the per-file reader lifecycle: BOM strip, header
// validation, and the row loop. Splitting it out of Stream keeps the
// goroutine literal in Stream small and lets each phase fail
// independently.
func streamRows(ctx context.Context, f *os.File, out chan<- Record) {
	br := bufio.NewReader(f)
	stripBOM(br)

	r := csv.NewReader(br)
	r.FieldsPerRecord = 3
	if !readAndValidateHeader(ctx, r, out) {
		return
	}
	for ctx.Err() == nil {
		if !nextRow(ctx, r, out) {
			return
		}
	}
}

func stripBOM(br *bufio.Reader) {
	if peeked, _ := br.Peek(len(utf8BOM)); bytes.Equal(peeked, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
}

// readAndValidateHeader returns false when the loop should stop
// (header missing, malformed, or context cancelled).
func readAndValidateHeader(ctx context.Context, r *csv.Reader, out chan<- Record) bool {
	header, err := r.Read()
	if err != nil {
		if err != io.EOF {
			sendRecord(ctx, out, Record{Err: fmt.Errorf("read header: %w", err)})
		}
		return false
	}
	if err := validateHeader(header); err != nil {
		sendRecord(ctx, out, Record{Err: err})
		return false
	}
	return true
}

// nextRow reads one record and forwards it. Returns false on EOF or
// context cancellation. Parse errors are forwarded but the loop
// continues (the csv.Reader recovers position on the next line).
func nextRow(ctx context.Context, r *csv.Reader, out chan<- Record) bool {
	row, err := r.Read()
	if err == io.EOF {
		return false
	}
	if err != nil {
		return sendRecord(ctx, out, Record{Err: err})
	}
	return sendRecord(ctx, out, Record{User: model.User{ID: row[0], Name: row[1], Email: row[2]}})
}

func validateHeader(header []string) error {
	if len(header) != len(expectedHeader) {
		return fmt.Errorf("header has %d columns, want %d", len(header), len(expectedHeader))
	}
	for i, want := range expectedHeader {
		got := strings.ToLower(strings.TrimSpace(header[i]))
		if got != want {
			return fmt.Errorf("header column %d is %q, want %q", i, header[i], want)
		}
	}
	return nil
}

func sendRecord(ctx context.Context, out chan<- Record, rec Record) bool {
	select {
	case out <- rec:
		return true
	case <-ctx.Done():
		return false
	}
}
