package pool

import "context"

type ctxKey struct{ name string }

var workerIDKey = ctxKey{"workerID"}

func withWorkerID(ctx context.Context, id int) context.Context {
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
