package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/middleware/logging"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/model/modeltest"
	"github.com/nostosx/agentx/tool"
)

func logRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("log=%q: %v", buf.String(), err)
	}
	return record
}

func TestLoggingModel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		failure error
		stream  bool
		level   string
	}{{"generate", nil, false, "INFO"}, {"stream", nil, true, "INFO"}, {"error", errors.New("failed"), false, "ERROR"}, {"cancellation", context.Canceled, false, "INFO"}, {"stream error", errors.New("failed"), true, "ERROR"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			m := modeltest.New(modeltest.Reply("done"))
			if tt.failure != nil {
				m = modeltest.New(modeltest.Fail(tt.failure))
			}
			wrapped := middleware.Wrap(m, logging.Model(slog.New(slog.NewJSONHandler(&buf, nil))))
			req := &model.Request{Model: "requested"}
			var callErr error
			if tt.stream {
				for _, err := range wrapped.Stream(t.Context(), req) {
					if err != nil {
						callErr = err
					}
				}
			} else {
				_, callErr = wrapped.Generate(t.Context(), req)
			}
			if !errors.Is(callErr, tt.failure) {
				t.Fatalf("logging changed model error: %v, want %v", callErr, tt.failure)
			}
			record := logRecord(t, &buf)
			if record["model"] != "requested" || record["level"] != tt.level || record["stream"] != tt.stream {
				t.Fatalf("record=%v", record)
			}
			if tt.failure == nil && record["output_tokens"] != float64(5) {
				t.Fatalf("missing usage=%v", record)
			}
		})
	}
}

func TestLogTools(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name           string
		result         *tool.Result
		failure        error
		message, level string
	}{
		{"success", tool.Text("ok"), nil, "tool call", "INFO"},
		{"model error", tool.Errorf("bad"), nil, "tool call", "INFO"},
		{"failure", nil, errors.New("bad"), "tool call failed", "WARN"},
		{"panic", nil, &tool.PanicError{Tool: "test", Value: "boom", Stack: []byte("stack trace")}, "tool panicked", "ERROR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			invoke := logging.Tools(slog.New(slog.NewJSONHandler(&buf, nil)))(func(context.Context, *tool.Call) (*tool.Result, error) { return tt.result, tt.failure })
			res, err := invoke(t.Context(), &tool.Call{Name: "test", ID: "id"})
			if res != tt.result || !errors.Is(err, tt.failure) {
				t.Fatalf("result changed=%v,%v", res, err)
			}
			record := logRecord(t, &buf)
			if record["msg"] != tt.message || record["level"] != tt.level || record["tool"] != "test" || record["call_id"] != "id" {
				t.Fatalf("record=%v", record)
			}
			if tt.name == "panic" && !strings.Contains(record["stack"].(string), "stack trace") {
				t.Fatal("panic stack missing")
			}
		})
	}
}

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

func echoRegistry(t *testing.T, fail error) *tool.Registry {
	t.Helper()
	echo := tool.Func("echo", "Echo text.", func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, fail
	})
	reg, err := tool.NewRegistry(echo)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestLoggingRuntimeMiddleware(t *testing.T) {
	t.Parallel()
	if got := logging.New(slog.Default()).Name(); got != "logging" {
		t.Fatalf("Name = %q", got)
	}
	toolFailure := errors.New("echo broke")
	for _, tc := range []struct {
		name     string
		toolErr  error
		toolMsg  string
		toolLvl  string
		wantRecs int
	}{
		{name: "success", toolMsg: "tool call", toolLvl: "INFO", wantRecs: 3},
		{name: "tool failure", toolErr: toolFailure, toolMsg: "tool call failed", toolLvl: "WARN", wantRecs: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			m := modeltest.New(modeltest.CallTools(modeltest.Call{ID: "c1", Name: "echo", Args: map[string]string{"text": "hi"}}), modeltest.Reply("done"))
			r := newTestRuntime(t, loop.Config{
				Name: "assistant", Model: m, Tools: echoRegistry(t, tc.toolErr),
				Middlewares: []loop.Middleware{logging.New(slog.New(slog.NewJSONHandler(&buf, nil)))},
			})
			res, err := loop.Collect(r.Run(t.Context(), &loop.Request{RunID: "run-1", SessionID: "session-1", Input: model.UserText("say hi")}), nil)
			if err != nil || res.Status != loop.StatusCompleted {
				t.Fatalf("Run = %+v, %v", res, err)
			}
			records := logRecords(t, &buf)
			if len(records) != tc.wantRecs {
				t.Fatalf("got %d records: %v", len(records), records)
			}
			want := []struct {
				msg, level string
				turn       float64
			}{{"model call", "INFO", 1}, {tc.toolMsg, tc.toolLvl, 1}, {"model call", "INFO", 2}}
			for i, w := range want {
				rec := records[i]
				if rec["msg"] != w.msg || rec["level"] != w.level || rec["turn"] != w.turn ||
					rec["run_id"] != "run-1" || rec["session_id"] != "session-1" || rec["agent"] != "assistant" {
					t.Errorf("record %d = %v, want %s/%s turn %v", i, rec, w.msg, w.level, w.turn)
				}
			}
			if records[1]["tool"] != "echo" || records[1]["call_id"] != "c1" {
				t.Errorf("tool record = %v", records[1])
			}
			if tc.toolErr != nil && records[1]["error"] != toolFailure.Error() {
				t.Errorf("tool failure record = %v", records[1])
			}
			if records[2]["finish_reason"] != string(model.FinishStop) || records[2]["output_tokens"] != float64(5) {
				t.Errorf("final model record = %v", records[2])
			}
		})
	}
}

func TestLoggingRuntimeModelFailures(t *testing.T) {
	t.Parallel()
	failure := &model.APIError{StatusCode: 500}
	for _, tc := range []struct {
		name  string
		step  func(cancel context.CancelFunc) modeltest.Step
		want  error
		level string
	}{
		{"provider error", func(context.CancelFunc) modeltest.Step { return modeltest.Fail(failure) }, failure, "ERROR"},
		// Cancellation is how a caller stops a run, not a failure of the
		// model, so it is not logged as an error.
		{"cancellation", func(cancel context.CancelFunc) modeltest.Step {
			return func(*model.Request) (*model.Response, error) { cancel(); return nil, context.Canceled }
		}, context.Canceled, "INFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var buf bytes.Buffer
			r := newTestRuntime(t, loop.Config{
				Name: "assistant", Model: modeltest.New(tc.step(cancel)),
				Middlewares: []loop.Middleware{logging.New(slog.New(slog.NewJSONHandler(&buf, nil)))},
			})
			res, err := loop.Collect(r.Run(ctx, &loop.Request{RunID: "run-2", SessionID: "s", Input: model.UserText("q")}), nil)
			if !errors.Is(err, tc.want) || res.Status != loop.StatusFailed {
				t.Fatalf("Run = %+v, %v", res, err)
			}
			records := logRecords(t, &buf)
			if len(records) != 1 {
				t.Fatalf("records = %v", records)
			}
			rec := records[0]
			if rec["msg"] != "model call failed" || rec["level"] != tc.level || rec["run_id"] != "run-2" || rec["turn"] != float64(1) || rec["error"] == nil {
				t.Fatalf("record = %v", rec)
			}
		})
	}
}
