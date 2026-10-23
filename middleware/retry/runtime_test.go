package retry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nostosx/agentx-contrib/middleware/logging"
	"github.com/nostosx/agentx-contrib/middleware/retry"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
)

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.Lines(buf.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func newTestRuntime(t *testing.T, cfg loop.Config) *loop.Runtime {
	t.Helper()
	r, err := loop.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRetryRuntimeMiddleware(t *testing.T) {
	t.Parallel()
	if got := retry.New(retry.Policy{}).Name(); got != "retry" {
		t.Fatalf("Name = %q", got)
	}
	policy := retry.Policy{InitialBackoff: time.Second, MaxBackoff: 10 * time.Second, Jitter: -1}
	transient := &model.APIError{StatusCode: 503}
	permanent := &model.APIError{StatusCode: 400}
	for _, tc := range []struct {
		name      string
		stream    bool
		steps     []modeltest.Step
		status    loop.Status
		wantErr   error
		wantCalls int
		elapsed   time.Duration
	}{
		{"transient generate", false, []modeltest.Step{modeltest.Fail(transient), modeltest.Fail(transient), modeltest.Reply("ok")}, loop.StatusCompleted, nil, 3, 3 * time.Second},
		{"permanent generate", false, []modeltest.Step{modeltest.Fail(permanent), modeltest.Reply("unexpected")}, loop.StatusFailed, permanent, 1, 0},
		{"exhausted generate", false, []modeltest.Step{modeltest.Fail(transient), modeltest.Fail(transient), modeltest.Fail(transient), modeltest.Reply("late")}, loop.StatusFailed, transient, 3, 3 * time.Second},
		{"transient stream", true, []modeltest.Step{modeltest.Fail(transient), modeltest.Reply("ok")}, loop.StatusCompleted, nil, 2, time.Second},
		{"permanent stream", true, []modeltest.Step{modeltest.Fail(permanent), modeltest.Reply("unexpected")}, loop.StatusFailed, permanent, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				m := modeltest.New(tc.steps...)
				r := newTestRuntime(t, loop.Config{Model: m, Stream: tc.stream, Middlewares: []loop.Middleware{retry.New(policy)}})
				start := time.Now()
				var deltas strings.Builder
				res, err := loop.Collect(r.Run(t.Context(), &loop.Request{Input: model.UserText("q")}), func(ev *loop.Event) {
					if ev.Type == loop.EventDelta {
						deltas.WriteString(ev.Text())
					}
				})
				if res.Status != tc.status || len(m.Requests()) != tc.wantCalls || time.Since(start) != tc.elapsed {
					t.Fatalf("Run = %+v, %v; calls = %d, elapsed = %v", res, err, len(m.Requests()), time.Since(start))
				}
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("Run error = %v, want %v", err, tc.wantErr)
					}
					return
				}
				if err != nil || res.Output != "ok" {
					t.Fatalf("Run = %+v, %v", res, err)
				}
				// Failed attempts emitted nothing, so the consumer sees the
				// successful stream once.
				if tc.stream && deltas.String() != "ok" {
					t.Fatalf("deltas = %q", deltas.String())
				}
			})
		})
	}
}

func TestRetryRuntimeStreamAfterFirstEvent(t *testing.T) {
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
	r := newTestRuntime(t, loop.Config{Model: base, Stream: true, Middlewares: []loop.Middleware{retry.New(retry.Policy{InitialBackoff: time.Millisecond})}})
	var deltas []string
	res, err := loop.Collect(r.Run(t.Context(), &loop.Request{Input: model.UserText("q")}), func(ev *loop.Event) {
		if ev.Type == loop.EventDelta {
			deltas = append(deltas, ev.Text())
		}
	})
	// Retrying would replay "partial" to a consumer that already saw it.
	if !errors.Is(err, failure) || res.Status != loop.StatusFailed || calls != 1 || len(deltas) != 1 || deltas[0] != "partial" {
		t.Fatalf("Run = %+v, %v; calls = %d, deltas = %q", res, err, calls, deltas)
	}
}

func TestRetryAndLoggingOrder(t *testing.T) {
	t.Parallel()
	transient := &model.APIError{StatusCode: 503}
	policy := retry.Policy{InitialBackoff: time.Second, Jitter: -1}
	for _, tc := range []struct {
		name   string
		mws    func(*slog.Logger) []loop.Middleware
		levels []string
	}{
		// Logging outermost sees one call that eventually succeeded.
		{"logging outside", func(l *slog.Logger) []loop.Middleware {
			return []loop.Middleware{logging.New(l), retry.New(policy)}
		}, []string{"INFO"}},
		// Logging inside the retry logs every attempt.
		{"logging inside", func(l *slog.Logger) []loop.Middleware {
			return []loop.Middleware{retry.New(policy), logging.New(l)}
		}, []string{"ERROR", "INFO"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				m := modeltest.New(modeltest.Fail(transient), modeltest.Reply("ok"))
				r := newTestRuntime(t, loop.Config{Model: m, Middlewares: tc.mws(slog.New(slog.NewJSONHandler(&buf, nil)))})
				res, err := loop.Collect(r.Run(t.Context(), &loop.Request{Input: model.UserText("q")}), nil)
				if err != nil || res.Output != "ok" {
					t.Fatalf("Run = %+v, %v", res, err)
				}
				var levels []string
				for _, rec := range logRecords(t, &buf) {
					levels = append(levels, rec["level"].(string))
				}
				if strings.Join(levels, ",") != strings.Join(tc.levels, ",") {
					t.Fatalf("levels = %v, want %v", levels, tc.levels)
				}
			})
		})
	}
}
