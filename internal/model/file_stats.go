package model

import "time"

// FileStats summarises one file's lifecycle through the ingest
// pipeline. Records is the count emitted by the Processor (parse
// errors excluded).
type FileStats struct {
	Path     string
	Records  int
	Duration time.Duration
}
