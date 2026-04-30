package handler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// WithPerWorkerCount increments the per-worker counter for every job
// that reaches a worker, regardless of outcome. Place this outermost.
func WithPerWorkerCount(s *Stats) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			s.IncWorker(pool.WorkerID(ctx))
			return next(ctx, file, u)
		}
	}
}

// WithLogging emits a structured log line per record. When verbose is
// false, the middleware is a no-op (zero allocation per call).
func WithLogging(logger *slog.Logger, verbose bool) Middleware {
	if !verbose {
		return func(next Handler) Handler { return next }
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			start := time.Now()
			out := next(ctx, file, u)
			logger.Info("processed",
				"worker", pool.WorkerID(ctx),
				"file", file,
				"id", u.ID,
				"outcome", out.String(),
				"dur", time.Since(start),
			)
			return out
		}
	}
}

// WithMetrics records the outcome in Stats. Place this outside the
// short-circuit middleware so it sees the final outcome.
func WithMetrics(s *Stats) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			out := next(ctx, file, u)
			switch out {
			case OutcomeOK:
				s.IncOK()
			case OutcomeDedup:
				s.IncDedup()
			case OutcomeCancelled:
				s.IncCancelled()
			case OutcomeError:
				s.IncParseErr()
			}
			return out
		}
	}
}

// WithCancelCheck short-circuits the chain if the context is already
// done by the time we reach this layer.
func WithCancelCheck() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			if ctx.Err() != nil {
				return OutcomeCancelled
			}
			return next(ctx, file, u)
		}
	}
}

// WithDedup short-circuits the chain if the user has been seen before.
func WithDedup(d Deduper) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			if !d.AddIfNew(u) {
				return OutcomeDedup
			}
			return next(ctx, file, u)
		}
	}
}

// WithProcess runs the actual per-record work. Context cancellation is
// reported as OutcomeCancelled; any other error becomes OutcomeError.
func WithProcess(fn ProcessFunc) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, file string, u user.User) Outcome {
			if err := fn(ctx, u); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return OutcomeCancelled
				}
				return OutcomeError
			}
			return next(ctx, file, u)
		}
	}
}
