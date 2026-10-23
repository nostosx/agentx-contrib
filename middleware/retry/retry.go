// Package retry retries failed model calls and embeddings of agentx with
// exponential backoff.
//
// Register it after tracing, so that a model span and its duration cover
// the retries, which are recorded as span events:
//
//	a := agent.MustNew("assistant",
//		agent.WithModel(m),
//		agent.WithMiddleware(middleware.Tracing(tracer), retry.New(retry.Policy{})),
//	)
package retry

import (
	"context"
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"time"

	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
)

// Policy configures New, Model and Embedder.
type Policy struct {
	// MaxAttempts includes the first call. Defaults to 3.
	MaxAttempts int
	// InitialBackoff defaults to 500ms; MaxBackoff to 30s. A server's
	// Retry-After is honored as the delay; when it exceeds MaxBackoff, the
	// error is returned instead of retrying before the server is ready.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Multiplier defaults to 2.
	Multiplier float64
	// Jitter randomizes each delay by up to this fraction. Defaults to 0.2;
	// set a negative value to disable.
	Jitter float64
	// Retryable decides whether an error is retried. Defaults to
	// model.IsRetryable.
	Retryable func(error) bool
}

func (p Policy) withDefaults() Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = 500 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 30 * time.Second
	}
	if p.Multiplier < 1 {
		p.Multiplier = 2
	}
	if p.Jitter == 0 {
		p.Jitter = 0.2
	}
	if p.Retryable == nil {
		p.Retryable = model.IsRetryable
	}
	return p
}

// delay returns the wait before the given retry (1-based), honoring a
// server-provided Retry-After. ok is false when Retry-After exceeds
// MaxBackoff.
func (p Policy) delay(retry int, err error) (d time.Duration, ok bool) {
	if after := model.RetryAfter(err); after > 0 {
		return after, after <= p.MaxBackoff
	}
	b := min(float64(p.InitialBackoff)*math.Pow(p.Multiplier, float64(retry-1)), float64(p.MaxBackoff))
	if p.Jitter > 0 {
		b *= 1 + p.Jitter*(2*rand.Float64()-1)
	}
	return time.Duration(b), true
}

// backoff decides whether to retry after the given attempt failed with err,
// and waits before the retry. It returns nil to retry, or the error to
// return: err itself, or err joined with the context's error if the context
// ends during the wait.
func (p Policy) backoff(ctx context.Context, attempt int, err error) error {
	if attempt >= p.MaxAttempts || !p.Retryable(err) {
		return err
	}
	d, ok := p.delay(attempt, err)
	if !ok {
		return err
	}
	middleware.AddEvent(ctx, "agentx.retry",
		middleware.Attr{Key: "agentx.retry.attempt", Value: attempt},
		middleware.Attr{Key: "agentx.retry.delay", Value: d.Seconds()},
		middleware.Attr{Key: "error.type", Value: middleware.ErrorType(err)})
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
	case <-t.C:
		return nil
	}
}

// New returns runtime middleware that retries failed model calls like
// Model.
func New(p Policy) loop.Middleware { return runtime{Model(p)} }

type runtime struct{ wrap middleware.Model }

func (runtime) Name() string { return "retry" }

func (r runtime) WrapModel(_ *loop.Run, next model.Model) model.Model { return r.wrap(next) }

// Model retries failed model calls with exponential backoff. A stream is
// retried only if it failed before yielding any event. If the context ends
// while waiting, the returned error matches both the context's error and
// the last failure.
//
// Each retry adds an "agentx.retry" event to the span of the call's context
// (see middleware.AddEvent) with the failed attempt (agentx.retry.attempt,
// from 1), the delay in seconds (agentx.retry.delay) and error.type.
// Wrapped by tracing, as when middleware.Tracing is registered before New,
// that is the model call's span.
func Model(p Policy) middleware.Model {
	p = p.withDefaults()
	return func(next model.Model) model.Model {
		return &middleware.Funcs{
			Next: next,
			GenerateFunc: func(ctx context.Context, req *model.Request) (*model.Response, error) {
				for attempt := 1; ; attempt++ {
					resp, err := next.Generate(ctx, req)
					if err == nil {
						return resp, nil
					}
					if err := p.backoff(ctx, attempt, err); err != nil {
						return resp, err
					}
				}
			},
			StreamFunc: func(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
				return func(yield func(*model.StreamEvent, error) bool) {
					for attempt := 1; ; attempt++ {
						started := false
						var failure error
						for ev, err := range next.Stream(ctx, req) {
							if err != nil && !started {
								failure = err
								break
							}
							started = true
							if !yield(ev, err) || err != nil {
								return
							}
						}
						if failure == nil {
							return
						}
						if err := p.backoff(ctx, attempt, failure); err != nil {
							yield(nil, err)
							return
						}
					}
				}
			},
		}
	}
}

// Embedder retries failed Embed calls of next with exponential backoff,
// deciding like Model: by default with model.IsRetryable, so rate limits,
// server errors and transport failures are retried and invalid input is
// not.
func Embedder(p Policy, next memory.Embedder) memory.Embedder {
	return embedder{p: p.withDefaults(), next: next}
}

type embedder struct {
	p    Policy
	next memory.Embedder
}

func (r embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for attempt := 1; ; attempt++ {
		vecs, err := r.next.Embed(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		if err := r.p.backoff(ctx, attempt, err); err != nil {
			return nil, err
		}
	}
}
