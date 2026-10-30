//go:build integration

package debug

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nostosx/agentx/eval"
	"github.com/nostosx/agentx/loop"
	"github.com/nostosx/agentx/telemetry"
)

func TestIntegrationBrowser(t *testing.T) {
	node, playwright := os.Getenv("AGENTX_IT_NODE"), os.Getenv("AGENTX_IT_PLAYWRIGHT")
	if node == "" || playwright == "" {
		t.Skip("set AGENTX_IT_NODE and AGENTX_IT_PLAYWRIGHT for Chromium/WebKit acceptance")
	}
	c := New(Options{})
	ctx, s := c.Start(t.Context(), "agent demo", telemetry.SpanKindInternal, telemetry.Attr{Key: "gen_ai.input.messages", Value: "PRIVATE_PROMPT"})
	_, child := c.Start(ctx, "<img src=x onerror=window.injected=true>", telemetry.SpanKindClient)
	child.RecordError(errors.New("PRIVATE_ERROR"))
	child.End()
	s.End()
	h, err := NewHandler(c, HTTPOptions{Authorize: func(r *http.Request) bool {
		cookie, err := r.Cookie("agentx_demo")
		return err == nil && cookie.Value == "local-fixture"
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := eval.Dataset[string, string]{Name: "local demo", Cases: []eval.Case[string, string]{{Name: "smoke", Input: "PRIVATE_INPUT"}}}
	for _, name := range []string{"baseline", "candidate"} {
		report, err := d.Evaluate(ctx, func(context.Context, string) (string, *loop.Result, error) {
			if name == "baseline" {
				return "PRIVATE_OUTPUT", nil, errors.New("PRIVATE_TASK_ERROR")
			}
			return "PRIVATE_OUTPUT", nil, nil
		}, eval.Experiment(name))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.PublishReport(name, report); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/inspect/", http.StripPrefix("/inspect", h))
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "agentx_demo", Value: "local-fixture", Path: "/inspect/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/inspect/", http.StatusSeeOther)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	_, source, _, _ := runtime.Caller(0)
	processCtx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(processCtx, node, filepath.Join(filepath.Dir(source), "testdata", "browser.mjs"), server.URL)
	cmd.Env = append(os.Environ(), "AGENTX_IT_PLAYWRIGHT="+playwright)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser acceptance: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
