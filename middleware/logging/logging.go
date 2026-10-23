// Package logging logs the model and tool calls of agentx runs with
// log/slog.
//
//	a := agent.MustNew("assistant",
//		agent.WithModel(m),
//		agent.WithMiddleware(logging.New(slog.Default())),
//	)
package logging

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"time"

	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/tool"
)

// New returns runtime middleware that logs each model and tool call
// like Model and Tools, with the run ID, session ID, agent and turn.
func New(log *slog.Logger) loop.Middleware { return logging{log} }

type logging struct{ log *slog.Logger }

func (logging) Name() string { return "logging" }

func (l logging) WrapModel(run *loop.Run, next model.Model) model.Model {
	return Model(runLogger(l.log, run))(next)
}

func (l logging) WrapTool(run *loop.Run, next tool.Invoker) tool.Invoker {
	return Tools(runLogger(l.log, run))(next)
}

func runLogger(log *slog.Logger, run *loop.Run) *slog.Logger {
	return log.With("run_id", run.ID(), "session_id", run.SessionID(), "agent", run.Agent(), "turn", run.Turn())
}

// Model logs each model call with its duration, finish reason and usage.
func Model(log *slog.Logger) middleware.Model {
	return func(next model.Model) model.Model {
		return &middleware.Funcs{
			Next: next,
			GenerateFunc: func(ctx context.Context, req *model.Request) (*model.Response, error) {
				start := time.Now()
				resp, err := next.Generate(ctx, req)
				logModel(ctx, log, req, resp, err, time.Since(start), false)
				return resp, err
			},
			StreamFunc: func(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
				return func(yield func(*model.StreamEvent, error) bool) {
					start := time.Now()
					var resp *model.Response
					var streamErr error
					defer func() { logModel(ctx, log, req, resp, streamErr, time.Since(start), true) }()
					for ev, err := range next.Stream(ctx, req) {
						if err != nil {
							streamErr = err
						} else if ev != nil && ev.Type == model.StreamDone {
							resp = ev.Response
						}
						if !yield(ev, err) {
							return
						}
					}
				}
			},
		}
	}
}

func logModel(ctx context.Context, log *slog.Logger, req *model.Request, resp *model.Response, err error, d time.Duration, stream bool) {
	attrs := []any{
		"model", req.Model,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"stream", stream,
		"duration", d,
	}
	if err != nil {
		level := slog.LevelError
		if errors.Is(err, context.Canceled) {
			level = slog.LevelInfo
		}
		log.Log(ctx, level, "model call failed", append(attrs, "error", err)...)
		return
	}
	if resp != nil {
		attrs = append(attrs,
			"response_model", resp.Model,
			"finish_reason", resp.FinishReason,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"cache_read_tokens", resp.Usage.CacheReadTokens,
			"tool_calls", len(resp.ToolCalls()),
		)
	}
	log.Log(ctx, slog.LevelInfo, "model call", attrs...)
}

// Tools logs each tool call. Returned *tool.PanicError values are logged with
// their stack; this middleware does not recover panics from next.
func Tools(log *slog.Logger) tool.Middleware {
	return func(next tool.Invoker) tool.Invoker {
		return func(ctx context.Context, call *tool.Call) (*tool.Result, error) {
			start := time.Now()
			res, err := next(ctx, call)
			attrs := []any{"tool", call.Name, "call_id", call.ID, "duration", time.Since(start)}
			pe, panicked := errors.AsType[*tool.PanicError](err)
			switch {
			case panicked:
				log.Log(ctx, slog.LevelError, "tool panicked", append(attrs, "panic", pe.Value, "stack", string(pe.Stack))...)
			case err != nil:
				log.Log(ctx, slog.LevelWarn, "tool call failed", append(attrs, "error", err)...)
			default:
				isErr := res != nil && res.IsError
				log.Log(ctx, slog.LevelInfo, "tool call", append(attrs, "is_error", isErr)...)
			}
			return res, err
		}
	}
}
