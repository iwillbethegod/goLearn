package csvr

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// Record carries either a parsed user or a parse error.
type Record struct {
	User user.User
	Err  error
}

// Stream opens path and returns a channel of records. The reader
// goroutine respects ctx: if the file is cancelled mid-read, the
// channel is closed and the file handle is released.
//
// Expected CSV header: id,name,email
func Stream(ctx context.Context, path string) (<-chan Record, error) {
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
		// Skip header.
		if _, err := r.Read(); err != nil {
			if err != io.EOF {
				select {
				case out <- Record{Err: fmt.Errorf("read header: %w", err)}:
				case <-ctx.Done():
				}
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
				select {
				case out <- Record{Err: err}:
				case <-ctx.Done():
				}
				continue
			}
			rec := Record{User: user.User{ID: row[0], Name: row[1], Email: row[2]}}
			select {
			case out <- rec:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
