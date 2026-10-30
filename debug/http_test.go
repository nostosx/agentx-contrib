package debug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/eval"
)

func TestHTTPAuthorizationReadOnlyAndReports(t *testing.T) {
	c := New(Options{})
	if _, err := NewHandler(c, HTTPOptions{}); err == nil {
		t.Fatal("implicit public handler")
	}
	h, err := NewHandler(c, HTTPOptions{Authorize: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer test" }})
	if err != nil {
		t.Fatal(err)
	}
	baseline := &eval.Report{Name: "baseline", Cases: []*eval.CaseResult{{Name: "case", Input: "private input", Output: "private output", Error: "private error"}}}
	candidate := &eval.Report{Name: "candidate", Cases: []*eval.CaseResult{{Name: "case", Evaluations: []eval.Evaluation{eval.Assertion("ok", true, "private reason")}}}}
	if err := h.PublishReport("baseline", baseline); err != nil {
		t.Fatal(err)
	}
	if err := h.PublishReport("candidate", candidate); err != nil {
		t.Fatal(err)
	}
	// Truncating identity fields could merge distinct cases or evaluations.
	if err := h.PublishReport("oversize", &eval.Report{Cases: []*eval.CaseResult{{Name: strings.Repeat("x", 257)}}}); err == nil {
		t.Fatal("oversized case identity was accepted")
	}
	baseline.Cases[0].Name = "mutated"
	for _, path := range []string{"/", "/viewer.js", "/traces", "/reports", "/compare?baseline=baseline&candidate=candidate"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatal(w.Code)
			}
			r.Header.Set("Authorization", "Bearer test")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "mutated") {
				t.Fatal("report content leaked")
			}
			if strings.HasPrefix(path, "/compare") {
				var comparison eval.Comparison
				if err := json.Unmarshal(w.Body.Bytes(), &comparison); err != nil || comparison.PassRate.Baseline != 0 || comparison.PassRate.Candidate != 1 {
					t.Fatalf("%+v %v", comparison, err)
				}
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
}
