// Package handler defines the per-record pipeline that runs inside
// each pool worker. The pipeline is a chain of Middleware composed
// around a terminal Handler, so concerns (cancel-check, dedup, the
// actual work, metrics, logging) live in independent units.
package handler

import (
	"context"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// Outcome is the terminal result of running a record through the chain.
type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeDedup
	OutcomeCancelled
	OutcomeError
)

func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeDedup:
		return "dedup"
	case OutcomeCancelled:
		return "cancelled"
	case OutcomeError:
		return "error"
	default:
		return "unknown"
	}
}

// Handler processes one record. The pipeline is composed via Middleware.
type Handler func(ctx context.Context, file string, u user.User) Outcome

// Middleware wraps a Handler with additional behavior.
type Middleware func(Handler) Handler

// Chain composes middleware around a terminal handler. The leftmost
// middleware is outermost and runs first.
func Chain(final Handler, mws ...Middleware) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Terminal is the no-op handler used as the base of the chain. It
// returns OutcomeOK so middleware that only short-circuits on failure
// (Dedup, CancelCheck) can rely on a default success.
func Terminal(_ context.Context, _ string, _ user.User) Outcome {
	return OutcomeOK
}

// Deduper is the contract for the dedup gate (satisfied by user.Store).
// Defining it here keeps the pipeline package free of concrete imports.
type Deduper interface {
	AddIfNew(u user.User) bool
}

// ProcessFunc is the per-record work function. It returns ctx.Err()
// if cancelled mid-flight and nil on success.
type ProcessFunc func(ctx context.Context, u user.User) error
