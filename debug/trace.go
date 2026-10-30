// Package debug provides bounded development traces and a read-only HTTP viewer.
// It uses agentx telemetry and evaluation contracts; it owns no Agent runtime.
package debug

import (
	"context"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/nostosx/agentx/telemetry"
)

// Options bounds retained completed spans. Content attributes are opt-in.
type Options struct {
	Capacity int
	// Attributes adds explicitly allowed scalar attributes to the safe defaults.
	// Do not allow prompts, credentials or tool payloads in a shared viewer.
	Attributes []string
}

// Event is a bounded, timestamped trace event.
type Event struct {
	Name       string         `json:"name"`
	At         time.Time      `json:"at"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Record describes a completed span; no raw error message is retained.
type Record struct {
	ID         string             `json:"id"`
	ParentID   string             `json:"parent_id,omitempty"`
	Name       string             `json:"name"`
	Kind       telemetry.SpanKind `json:"kind"`
	Started    time.Time          `json:"started"`
	Ended      time.Time          `json:"ended"`
	ErrorType  string             `json:"error_type,omitempty"`
	Attributes map[string]any     `json:"attributes,omitempty"`
	Events     []Event            `json:"events,omitempty"`
}

// Collector implements telemetry.Tracer. It must not be copied after use.
type Collector struct {
	mu       sync.RWMutex
	capacity int
	records  []Record
	next     int
	allowed  map[string]bool
}

// New creates a collector; nonpositive capacity selects 256, capped at 10000.
func New(o Options) *Collector {
	n := o.Capacity
	if n <= 0 {
		n = 256
	}
	if n > 10000 {
		n = 10000
	}
	allowed := map[string]bool{}
	for _, k := range []string{"agentx.run_id", "agentx.turn", "agentx.run.status", "gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "gen_ai.response.model", "gen_ai.tool.name", "error.type"} {
		allowed[k] = true
	}
	for _, k := range o.Attributes {
		if len(k) <= 128 {
			allowed[k] = true
		}
	}
	return &Collector{capacity: n, allowed: allowed}
}

type parentKey struct{}
type parent struct {
	owner *Collector
	id    string
}
type span struct {
	mu     sync.Mutex
	owner  *Collector
	record Record
	ended  bool
}

// Start implements telemetry.Tracer. The context carries the span for events.
func (c *Collector) Start(ctx context.Context, name string, kind telemetry.SpanKind, attrs ...telemetry.Attr) (context.Context, telemetry.Span) {
	s := &span{owner: c, record: Record{ID: uuid.New().String(), Name: short(name), Kind: kind, Started: time.Now(), Attributes: map[string]any{}}}
	if p, ok := ctx.Value(parentKey{}).(parent); ok && p.owner == c {
		s.record.ParentID = p.id
	}
	s.SetAttributes(attrs...)
	ctx = context.WithValue(ctx, parentKey{}, parent{c, s.record.ID})
	return telemetry.ContextWithSpan(ctx, s), s
}

func short(s string) string {
	if len(s) > 256 {
		s = s[:256]
	}
	return strings.ToValidUTF8(s, "")
}

func (c *Collector) attributes(dst map[string]any, attrs []telemetry.Attr) {
	for _, a := range attrs {
		if !c.allowed[a.Key] {
			continue
		}
		if _, exists := dst[a.Key]; !exists && len(dst) >= 32 {
			continue
		}
		switch v := a.Value.(type) {
		case string:
			dst[a.Key] = short(v)
		case bool, int, int32, int64, uint, uint32, uint64:
			dst[a.Key] = v
		case float64:
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				dst[a.Key] = v
			}
		case float32:
			if !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) {
				dst[a.Key] = v
			}
		}
	}
}

func (s *span) SetAttributes(attrs ...telemetry.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.owner.attributes(s.record.Attributes, attrs)
	}
}
func (s *span) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.record.ErrorType = short(telemetry.ErrorType(err))
	}
}
func (s *span) AddEvent(name string, attrs ...telemetry.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended && len(s.record.Events) < 64 {
		a := map[string]any{}
		s.owner.attributes(a, attrs)
		s.record.Events = append(s.record.Events, Event{Name: short(name), At: time.Now(), Attributes: a})
	}
}
func (s *span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.record.Ended = time.Now()
	c := s.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) < c.capacity {
		c.records = append(c.records, s.record)
	} else {
		c.records[c.next] = s.record
		c.next = (c.next + 1) % c.capacity
	}
}

// Snapshot returns independent records ordered by completion, oldest first.
func (c *Collector) Snapshot() []Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Record, len(c.records))
	for i := range out {
		j := i
		if len(c.records) == c.capacity {
			j = (c.next + i) % len(c.records)
		}
		v := c.records[j]
		v.Attributes = maps.Clone(v.Attributes)
		v.Events = append([]Event(nil), v.Events...)
		for k := range v.Events {
			v.Events[k].Attributes = maps.Clone(v.Events[k].Attributes)
		}
		out[i] = v
	}
	return out
}

var _ telemetry.Tracer = (*Collector)(nil)
var _ telemetry.EventSpan = (*span)(nil)
