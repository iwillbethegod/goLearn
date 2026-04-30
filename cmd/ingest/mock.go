package main

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// makeMockProcessRow returns a ProcessFunc that sleeps for a uniform
// random duration in [minD, maxD], cancellable via ctx. Without the
// cancel-aware select, per-file cancellation would lag by up to one
// full sleep per in-flight record.
func makeMockProcessRow(minD, maxD time.Duration) handler.ProcessFunc {
	if minD < 0 {
		minD = 0
	}
	if maxD <= minD {
		maxD = minD + time.Microsecond
	}
	span := int64(maxD - minD)
	return func(ctx context.Context, _ user.User) error {
		d := minD + time.Duration(rand.Int64N(span))
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
