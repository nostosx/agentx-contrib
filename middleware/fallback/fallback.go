// Package fallback routes failed model calls through an explicit target list.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"

	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/telemetry"
)

// Target selects a model adapter and optionally overrides Request.Model.
// Set ModelName for cross-provider routing when the request contains a model ID
// that the alternative provider does not recognize.
type Target struct {
	Model     model.Model
	ModelName string
}

// Options controls routing. By default only model.IsRetryable failures advance
// to another target. Cancellation never advances. No attempt is retried on the
// same target; compose the existing retry decorator when that policy is wanted.
type Options struct{ ShouldFallback func(error) bool }

// Model tries targets in order. It is safe for concurrent use when targets are.
// Request.Model overrides belong to each target, and routing never mutates the
// caller's request. A stream cannot switch providers after yielding any event.
type Model struct {
	targets        []Target
	shouldFallback func(error) bool
}

var _ model.Model = (*Model)(nil)

// New returns a router. Targets are copied; adapters remain application-owned.
func New(targets []Target, opts Options) (*Model, error) {
	if len(targets) == 0 {
		return nil, errors.New("fallback: at least one target is required")
	}
	for _, target := range targets {
		if target.Model == nil {
			return nil, errors.New("fallback: nil target model")
		}
	}
	if opts.ShouldFallback == nil {
		opts.ShouldFallback = model.IsRetryable
	}
	return &Model{targets: slices.Clone(targets), shouldFallback: opts.ShouldFallback}, nil
}

func request(req *model.Request, target Target) *model.Request {
	r := req.Clone()
	if target.ModelName != "" {
		r.Model = target.ModelName
	}
	return r
}

func (m *Model) advance(ctx context.Context, index int, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && !model.IsRetryable(err) {
		return false
	}
	if index+1 >= len(m.targets) || !m.shouldFallback(err) {
		return false
	}
	telemetry.AddEvent(ctx, "agentx.fallback", telemetry.Attr{Key: "agentx.fallback.target", Value: index + 1}, telemetry.Attr{Key: "error.type", Value: telemetry.ErrorType(err)})
	return true
}

// terminal keeps every cause while classifying the outcome by the last target,
// rather than letting an earlier transient failure hide a permanent final one.
func terminal(ctx context.Context, failures []error, last error) error {
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
		last = err
	}
	ordered := slices.Clone(failures)
	slices.Reverse(ordered)
	err := errors.Join(ordered...)
	if !model.IsRetryable(last) {
		return model.Permanent(err)
	}
	return err
}

// Generate returns the first successful response, preserving the final partial
// response on failure and all attempted errors through errors.Join.
func (m *Model) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	if req == nil {
		return nil, model.Permanent(errors.New("fallback: request is required"))
	}
	var failures []error
	for i, target := range m.targets {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(append(failures, err)...)
		}
		resp, err := target.Model.Generate(ctx, request(req, target))
		if err == nil {
			return resp, nil
		}
		failures = append(failures, fmt.Errorf("fallback: target %d: %w", i, err))
		if !m.advance(ctx, i, err) {
			return resp, terminal(ctx, failures, err)
		}
	}
	panic("unreachable")
}

// Stream switches targets only before the first yielded event. A stream that
// ends without StreamDone is a failure, including a completely empty stream.
func (m *Model) Stream(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, model.Permanent(errors.New("fallback: request is required")))
			return
		}
		var failures []error
		for i, target := range m.targets {
			if err := ctx.Err(); err != nil {
				yield(nil, errors.Join(append(failures, err)...))
				return
			}
			started, done := false, false
			var failure error
			for event, err := range target.Model.Stream(ctx, request(req, target)) {
				if err != nil {
					failure = err
					break
				}
				if event == nil {
					failure = errors.New("fallback: nil stream event")
					break
				}
				started = true
				done = event.Type == model.StreamDone
				if !yield(event, nil) {
					return
				}
				if done {
					return
				}
			}
			if failure == nil && !done {
				failure = fmt.Errorf("fallback: incomplete stream: %w", io.ErrUnexpectedEOF)
			}
			failures = append(failures, fmt.Errorf("fallback: target %d: %w", i, failure))
			if started || !m.advance(ctx, i, failure) {
				yield(nil, terminal(ctx, failures, failure))
				return
			}
		}
	}
}
