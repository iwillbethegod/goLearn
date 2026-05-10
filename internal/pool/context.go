package pool

import "context"

type ctxKey struct{ name string }

var workerIDKey = ctxKey{"workerID"}

// WithWorkerID returns a child ctx tagged with the given worker id.
// Pool uses this internally to label per-worker work; exported so
// test code can simulate worker context without spinning up a Pool.
func WithWorkerID(ctx context.Context, id int) context.Context {
	return context.WithValue(ctx, workerIDKey, id)
}

// WorkerID returns the ID of the worker handling the current context,
// or 0 if ctx was not produced by a Pool.
func WorkerID(ctx context.Context) int {
	if v, ok := ctx.Value(workerIDKey).(int); ok {
		return v
	}
	return 0
}
