package model

import "time"

// FileStats summarises one file's lifecycle through the ingest
// pipeline.
//
//   - Records: rows the Processor emitted (parse errors excluded).
//   - Handled: rows that finished as OutcomeOK or OutcomeDedup, i.e.
//     tokens that were "spent" on real work. Day-4's token gate uses
//     (Records − Handled) to decide how many tokens to refund after
//     a partial failure or cancel.
type FileStats struct {
	Path     string
	Records  int
	Handled  int
	Duration time.Duration
}
