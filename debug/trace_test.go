package debug

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nostosx/agentx/agent"
	"github.com/nostosx/agentx/middleware"
	"github.com/nostosx/agentx/model/modeltest"
	"github.com/nostosx/agentx/telemetry"
)

func TestTraceHierarchyEventsPrivacyAndCopies(t *testing.T) {
	c := New(Options{Capacity: 2})
	ctx, parent := c.Start(t.Context(), "agent", telemetry.SpanKindInternal, telemetry.Attr{Key: "agentx.run_id", Value: "run"}, telemetry.Attr{Key: "gen_ai.input.messages", Value: "private prompt"})
	childCtx, child := c.Start(ctx, "tool", telemetry.SpanKindClient)
	telemetry.AddEvent(childCtx, "fallback", telemetry.Attr{Key: "gen_ai.request.model", Value: "secondary"})
	child.RecordError(errors.New("private error message"))
	child.End()
	child.End()
	parent.End()
	out := c.Snapshot()
	if len(out) != 2 || out[0].ParentID != out[1].ID || len(out[0].Events) != 1 {
		t.Fatalf("%+v", out)
	}
	b, err := json.Marshal(out)
	if err != nil || strings.Contains(string(b), "private") {
		t.Fatalf("privacy: %s %v", b, err)
	}
	out[1].Attributes["agentx.run_id"] = "mutated"
	out[0].Events[0].Attributes["gen_ai.request.model"] = "mutated"
	fresh := c.Snapshot()
	if fresh[1].Attributes["agentx.run_id"] != "run" || fresh[0].Events[0].Attributes["gen_ai.request.model"] != "secondary" {
		t.Fatal("snapshot aliases collector")
	}
	_, s := c.Start(ctx, "new", telemetry.SpanKindInternal)
	s.End()
	if len(c.Snapshot()) != 2 || c.Snapshot()[1].Name != "new" {
		t.Fatal("ring capacity/order")
	}
}

func TestAgentTracingComposition(t *testing.T) {
	c := New(Options{})
	a := agent.MustNew("assistant", agent.WithModel(modeltest.New(modeltest.Reply("PRIVATE_OUTPUT"))), agent.WithInstructions("PRIVATE_SYSTEM"), agent.WithMiddleware(middleware.Tracing(c, middleware.RecordContent())))
	if _, err := a.Ask(t.Context(), "PRIVATE_INPUT"); err != nil {
		t.Fatal(err)
	}
	out := c.Snapshot()
	if len(out) < 2 {
		t.Fatalf("Agent/model spans missing: %+v", out)
	}
	ids := map[string]bool{}
	for _, span := range out {
		ids[span.ID] = true
	}
	children := 0
	for _, span := range out {
		if span.ParentID != "" {
			if !ids[span.ParentID] {
				t.Fatal("missing parent")
			}
			children++
		}
	}
	if children == 0 {
		t.Fatal("Agent/model hierarchy missing")
	}
	b, err := json.Marshal(out)
	if err != nil || strings.Contains(string(b), "PRIVATE_") {
		t.Fatalf("content was retained: %s %v", b, err)
	}
}

func TestConcurrentSpansAndBounds(t *testing.T) {
	c := New(Options{Capacity: 20})
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			ctx, s := c.Start(t.Context(), strings.Repeat("x", 1000), telemetry.SpanKindInternal)
			for range 100 {
				telemetry.AddEvent(ctx, "event")
			}
			s.End()
			_ = c.Snapshot()
		})
	}
	wg.Wait()
	out := c.Snapshot()
	if len(out) != 20 {
		t.Fatal(len(out))
	}
	for _, r := range out {
		if len(r.Name) > 256 || len(r.Events) != 64 {
			t.Fatal("unbounded trace")
		}
	}
}
