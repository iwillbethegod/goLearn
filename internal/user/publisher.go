package user

import (
	"context"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
)

// Publisher is the side-effect boundary the Service uses to fan out
// domain events. Implementations live in internal/events/<transport>;
// the no-op default keeps Service.Register working without a broker
// (handy in tests and in cmd/ingest).
type Publisher interface {
	PublishUserCreated(ctx context.Context, u model.User) error
}

// Option configures a *Service at construction time. The variadic
// shape preserves backwards compatibility: existing
// user.NewService(repo, logger, metrics) call sites compile unchanged.
type Option func(*Service)

// WithPublisher wires p into the Service. Register publishes
// user.created after the DB row is committed; failures are logged but
// do NOT fail Register (the user is already persisted, so refusing the
// HTTP response would be misleading). Use the outbox pattern in a
// future iteration for stricter at-least-once semantics.
func WithPublisher(p Publisher) Option {
	return func(s *Service) {
		if p != nil {
			s.publisher = p
		}
	}
}

// noopPublisher is the default Service.publisher when none is wired.
// It matches the contract — every method returns nil.
type noopPublisher struct{}

func (noopPublisher) PublishUserCreated(context.Context, model.User) error { return nil }
