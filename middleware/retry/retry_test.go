package retry_test

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nostosx/agentx-contrib/middleware/retry"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
)

func TestRetryGenerate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                string
		failure             error
		attempts, wantCalls int
		wantErr             bool
	}{
		{"transient", &model.APIError{StatusCode: 503}, 3, 3, false},
		{"not retryable", &model.APIError{StatusCode: 400}, 3, 1, true},
		{"attempt limit", &model.APIError{StatusCode: 429}, 2, 2, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				m := modeltest.New(modeltest.Fail(tt.failure), modeltest.Fail(tt.failure), modeltest.Reply("ok"))
				wrapped := middleware.Wrap(m, retry.Model(retry.Policy{MaxAttempts: tt.attempts, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second, Jitter: -1}))
				start := time.Now()
				resp, err := wrapped.Generate(t.Context(), &model.Request{})
				if (err != nil) != tt.wantErr || len(m.Requests()) != tt.wantCalls {
					t.Fatalf("Generate=%+v,%v calls=%d", resp, err, len(m.Requests()))
				}
				if !tt.wantErr && (resp.Text() != "ok" || time.Since(start) != 3*time.Second) {
					t.Fatalf("response=%+v elapsed=%v", resp, time.Since(start))
				}
			})
		})
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	t.Parallel()
	policy := retry.Model(retry.Policy{InitialBackoff: time.Second, MaxBackoff: 2 * time.Second, Jitter: -1})
	synctest.Test(t, func(t *testing.T) {
		m := modeltest.New(modeltest.Fail(&model.APIError{StatusCode: 429, RetryAfter: 1500 * time.Millisecond}), modeltest.Reply("ok"))
		start := time.Now()
		_, err := middleware.Wrap(m, policy).Generate(t.Context(), &model.Request{})
		if err != nil || time.Since(start) != 1500*time.Millisecond {
			t.Fatalf("Generate=%v elapsed=%v", err, time.Since(start))
		}
	})
	// A retry before the server's hint would fail again, so a hint beyond
	// MaxBackoff ends the retries.
	synctest.Test(t, func(t *testing.T) {
		failure := &model.APIError{StatusCode: 429, RetryAfter: 10 * time.Second}
		m := modeltest.New(modeltest.Fail(failure), modeltest.Reply("ok"))
		start := time.Now()
		_, err := middleware.Wrap(m, policy).Generate(t.Context(), &model.Request{})
		if !errors.Is(err, failure) || len(m.Requests()) != 1 || time.Since(start) != 0 {
			t.Fatalf("Generate=%v requests=%d elapsed=%v", err, len(m.Requests()), time.Since(start))
		}
	})
}

func TestRetrySkipsLocalErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("openai: response has no choices")
	m := modeltest.New(modeltest.Fail(failure), modeltest.Reply("ok"))
	_, err := middleware.Wrap(m, retry.Model(retry.Policy{})).Generate(t.Context(), &model.Request{})
	if !errors.Is(err, failure) || len(m.Requests()) != 1 {
		t.Fatalf("Generate=%v requests=%d", err, len(m.Requests()))
	}
}

func TestRetryStreamBeforeFirstEvent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m := modeltest.New(modeltest.Fail(&model.APIError{StatusCode: 503}), modeltest.Reply("hello"))
		wrapped := middleware.Wrap(m, retry.Model(retry.Policy{InitialBackoff: time.Second, Jitter: -1}))
		var text string
		var done bool
		for ev, err := range wrapped.Stream(t.Context(), &model.Request{}) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Delta != nil {
				text += ev.Delta.Text
			}
			if ev.Type == model.StreamDone {
				done = true
			}
		}
		if text != "hello" || !done || len(m.Requests()) != 2 {
			t.Fatalf("stream text=%q done=%v calls=%d", text, done, len(m.Requests()))
		}
	})
}

func TestRetryStreamDoesNotReplayAfterEvent(t *testing.T) {
	t.Parallel()
	failure := &model.APIError{StatusCode: 503}
	calls := 0
	base := &middleware.Funcs{StreamFunc: func(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
		return func(yield func(*model.StreamEvent, error) bool) {
			calls++
			if !yield(&model.StreamEvent{Type: model.StreamDelta, Delta: &model.Part{Type: model.PartText, Text: "partial"}}, nil) {
				return
			}
			yield(nil, failure)
		}
	}}
	wrapped := middleware.Wrap(base, retry.Model(retry.Policy{}))
	events := 0
	var gotErr error
	for _, err := range wrapped.Stream(t.Context(), &model.Request{}) {
		events++
		if err != nil {
			gotErr = err
		}
	}
	if calls != 1 || events != 2 || !errors.Is(gotErr, failure) {
		t.Fatalf("calls=%d events=%d err=%v", calls, events, gotErr)
	}
	calls = 0
	for _, err := range wrapped.Stream(t.Context(), &model.Request{}) {
		if err != nil {
			t.Fatalf("early stop stream: %v", err)
		}
		break
	}
	if calls != 1 {
		t.Fatalf("early stop calls=%d", calls)
	}
}

func TestRetryStreamExhaustion(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		failure := &model.APIError{StatusCode: 503}
		m := modeltest.New(modeltest.Fail(failure), modeltest.Fail(failure))
		wrapped := middleware.Wrap(m, retry.Model(retry.Policy{MaxAttempts: 2, InitialBackoff: time.Second, Jitter: -1}))
		count := 0
		for ev, err := range wrapped.Stream(t.Context(), &model.Request{}) {
			count++
			if ev != nil || !errors.Is(err, failure) {
				t.Fatalf("terminal event=%v,%v", ev, err)
			}
		}
		if count != 1 || len(m.Requests()) != 2 {
			t.Fatalf("events=%d requests=%d", count, len(m.Requests()))
		}
	})
}

func TestRetryCancellationStopsBackoff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		failure := &model.APIError{StatusCode: 503}
		m := modeltest.New(func(*model.Request) (*model.Response, error) { cancel(); return nil, failure }, modeltest.Reply("unexpected"))
		start := time.Now()
		_, err := middleware.Wrap(m, retry.Model(retry.Policy{InitialBackoff: time.Hour, Jitter: -1})).Generate(ctx, &model.Request{})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) || len(m.Requests()) != 1 || time.Since(start) != 0 {
			t.Fatalf("cancellation err=%v requests=%d duration=%v", err, len(m.Requests()), time.Since(start))
		}
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		failure := &model.APIError{StatusCode: 503}
		m := modeltest.New(func(*model.Request) (*model.Response, error) { cancel(); return nil, failure }, modeltest.Reply("unexpected"))
		var err error
		for _, e := range middleware.Wrap(m, retry.Model(retry.Policy{InitialBackoff: time.Hour, Jitter: -1})).Stream(ctx, &model.Request{}) {
			err = e
		}
		if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) || len(m.Requests()) != 1 {
			t.Fatalf("stream cancellation err=%v requests=%d", err, len(m.Requests()))
		}
	})
}

type embedFunc func(context.Context, []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func TestRetryEmbedder(t *testing.T) {
	t.Parallel()
	policy := retry.Policy{MaxAttempts: 3, InitialBackoff: time.Second, Jitter: -1}
	for _, tc := range []struct {
		name      string
		err       error
		wantCalls int
		wantErr   bool
	}{
		{name: "retryable then success", err: &model.APIError{StatusCode: http.StatusTooManyRequests}, wantCalls: 2},
		{name: "permanent", err: &model.APIError{StatusCode: http.StatusBadRequest}, wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				e := retry.Embedder(policy, embedFunc(func(context.Context, []string) ([][]float32, error) {
					calls++
					if calls == 1 {
						return nil, tc.err
					}
					if tc.wantErr {
						t.Error("retried a permanent error")
					}
					return [][]float32{{1}}, nil
				}))
				start := time.Now()
				vecs, err := e.Embed(t.Context(), []string{"x"})
				if calls != tc.wantCalls || (err != nil) != tc.wantErr {
					t.Fatalf("calls = %d, vecs = %v, err = %v", calls, vecs, err)
				}
				if tc.wantErr && !errors.Is(err, tc.err) {
					t.Fatalf("error = %v", err)
				}
				if !tc.wantErr && !reflect.DeepEqual(vecs, [][]float32{{1}}) {
					t.Errorf("vectors = %v, want [[1]]", vecs)
				}
				if elapsed := time.Since(start); elapsed != time.Duration(tc.wantCalls-1)*time.Second {
					t.Errorf("backoff = %v, want %v", elapsed, time.Duration(tc.wantCalls-1)*time.Second)
				}
			})
		})
	}
}
