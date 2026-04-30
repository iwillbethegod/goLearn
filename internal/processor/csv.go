package processor

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// CSVProcessor reads files with header `id,name,email`.
type CSVProcessor struct{}

func (CSVProcessor) Name() string         { return "csv" }
func (CSVProcessor) Extensions() []string { return []string{".csv"} }

func (CSVProcessor) Stream(ctx context.Context, path string) (<-chan Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	out := make(chan Record)
	go func() {
		defer close(out)
		defer f.Close()

		r := csv.NewReader(f)
		r.FieldsPerRecord = 3
		if _, err := r.Read(); err != nil {
			if err != io.EOF {
				sendRecord(ctx, out, Record{Err: fmt.Errorf("read header: %w", err)})
			}
			return
		}
		for {
			if ctx.Err() != nil {
				return
			}
			row, err := r.Read()
			if err == io.EOF {
				return
			}
			if err != nil {
				if !sendRecord(ctx, out, Record{Err: err}) {
					return
				}
				continue
			}
			rec := Record{User: user.User{ID: row[0], Name: row[1], Email: row[2]}}
			if !sendRecord(ctx, out, rec) {
				return
			}
		}
	}()
	return out, nil
}

func sendRecord(ctx context.Context, out chan<- Record, rec Record) bool {
	select {
	case out <- rec:
		return true
	case <-ctx.Done():
		return false
	}
}
