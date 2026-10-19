package naive

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

type readinessKey struct{}

// WithReadiness reports whether the managed helper accepts local connections.
// The callback must be safe to call from the helper's supervisor goroutine.
func WithReadiness(ctx context.Context, report func(bool)) context.Context {
	return context.WithValue(ctx, readinessKey{}, report)
}

func reportReadiness(ctx context.Context, ready bool) {
	if report, ok := ctx.Value(readinessKey{}).(func(bool)); ok && report != nil {
		report(ready)
	}
}

// StartWithRetry keeps the reverse engine stopped until its helper starts.
// Retrying the whole start also covers preflight failures and occupied ports,
// which happen before the child supervisor exists. Cancellation ends the wait.
func StartWithRetry(ctx context.Context, log *logrus.Logger, start func() (*Helper, error)) (*Helper, error) {
	if log == nil {
		log = logrus.StandardLogger()
	}
	for ctx.Err() == nil {
		reportReadiness(ctx, false)
		h, err := start()
		if err == nil {
			return h, nil
		}
		if ctx.Err() != nil {
			break
		}
		log.Warnf("managed transport helper could not start: %v; retrying in one second", err)
		if !sleep(ctx, time.Second) {
			break
		}
	}
	return nil, ctx.Err()
}
