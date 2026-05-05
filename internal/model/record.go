package model

// Record is the unit of work flowing from a Processor (CSV today,
// JSON / Parquet tomorrow) into the ingest pipeline. Err is non-nil
// for parse failures; the runner logs and skips them. User and Err
// are mutually exclusive.
type Record struct {
	User User
	Err  error
}
