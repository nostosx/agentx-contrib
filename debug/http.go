package debug

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/nostosx/agentx/eval"
)

//go:embed viewer.html
var viewer string

//go:embed viewer.js
var javascript string

// HTTPOptions requires application authorization for every viewer route.
type HTTPOptions struct {
	Authorize func(*http.Request) bool
}

// Handler serves the trace viewer and evaluation summaries. Mount with
// http.StripPrefix at a path ending in slash. It exposes no execution endpoint.
type Handler struct {
	collector *Collector
	authorize func(*http.Request) bool
	mu        sync.RWMutex
	reports   map[string]*eval.Report
}

// NewHandler requires an explicit authorization function; there is no listener,
// global mux registration, default credentials or implicit public access.
func NewHandler(c *Collector, o HTTPOptions) (*Handler, error) {
	if c == nil || o.Authorize == nil {
		return nil, errors.New("debug: collector and authorization are required")
	}
	return &Handler{collector: c, authorize: o.Authorize, reports: map[string]*eval.Report{}}, nil
}

// PublishReport copies only evaluation outcomes and aggregate statistics;
// inputs, outputs, expected values, metadata, reasons and error messages are
// discarded. It retains at most 32 reports, each with at most 10000 cases.
// The caller must not mutate r during this call.
func (h *Handler) PublishReport(id string, r *eval.Report) error {
	if id == "" || len(id) > 128 || r == nil || len(r.Cases) > 10000 {
		return errors.New("debug: invalid report ID or report size")
	}
	cp := *r
	cp.Name = short(r.Name)
	cp.Dataset = short(r.Dataset)
	cp.Cases = nil
	cp.Summary.Labels = nil
	for _, c := range r.Cases {
		if c == nil {
			return errors.New("debug: nil evaluation case")
		}
		if len(c.Name) > 256 {
			return errors.New("debug: evaluation case name exceeds 256 bytes")
		}
		v := *c
		v.Input = nil
		v.Expected = nil
		v.Output = nil
		v.Metadata = nil
		if v.Error != "" {
			v.Error = "error"
		}
		v.EvaluatorErrors = nil
		for _, e := range c.EvaluatorErrors {
			v.EvaluatorErrors = append(v.EvaluatorErrors, eval.EvaluatorError{Evaluator: short(e.Evaluator), Error: "error"})
		}
		v.Evaluations = nil
		for _, e := range c.Evaluations {
			if e.Kind == eval.KindLabel {
				continue
			}
			if len(e.Name) > 256 {
				return errors.New("debug: evaluation name exceeds 256 bytes")
			}
			e.Evaluator = short(e.Evaluator)
			e.Reason = ""
			e.Label = ""
			v.Evaluations = append(v.Evaluations, e)
		}
		cp.Cases = append(cp.Cases, &v)
	}
	b, err := json.Marshal(&cp)
	if err != nil {
		return errors.New("debug: invalid report values")
	}
	if len(b) > 1<<20 {
		return errors.New("debug: report exceeds 1 MiB")
	}
	var frozen eval.Report
	if err := json.Unmarshal(b, &frozen); err != nil {
		return errors.New("debug: copy report")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.reports[id]; !ok && len(h.reports) >= 32 {
		return errors.New("debug: report capacity reached")
	}
	h.reports[id] = &frozen
	return nil
}

// ServeHTTP implements http.Handler. Every response disables shared caching.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, viewer)
	case "/viewer.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = fmt.Fprint(w, javascript)
	case "/traces":
		writeJSON(w, h.collector.Snapshot())
	case "/reports":
		h.mu.RLock()
		defer h.mu.RUnlock()
		type summary struct {
			ID      string             `json:"id"`
			Name    string             `json:"name"`
			Dataset string             `json:"dataset"`
			Summary eval.Summary       `json:"summary"`
			Cases   []eval.CaseSummary `json:"cases"`
		}
		out := []summary{}
		for id, r := range h.reports {
			out = append(out, summary{ID: id, Name: r.Name, Dataset: r.Dataset, Summary: r.Summary, Cases: r.ByCase()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		writeJSON(w, out)
	case "/compare":
		h.mu.RLock()
		defer h.mu.RUnlock()
		b, c := h.reports[r.URL.Query().Get("baseline")], h.reports[r.URL.Query().Get("candidate")]
		if b == nil || c == nil {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		writeJSON(w, eval.Compare(b, c))
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "invalid debug data", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}
