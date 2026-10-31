//go:build integration

package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/nostosx/agentx/browser"
	"github.com/nostosx/agentx/web"
)

type fixtureExtractor struct{}

func (fixtureExtractor) Extract(_ context.Context, u string, b []byte) (*web.Document, error) {
	return &web.Document{URL: u, Text: string(b)}, nil
}

func TestIntegrationChromium(t *testing.T) {
	binary := os.Getenv("AGENTX_IT_BROWSER_BINARY")
	if binary == "" {
		t.Skip("set AGENTX_IT_BROWSER_BINARY to disposable Chromium")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			w.Header().Set("Content-Disposition", `attachment; filename="evidence.txt"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "download evidence")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Fixture</title></head><body><input id="name"><button id="apply" onclick="document.querySelector('#result').textContent=document.querySelector('#name').value">Apply</button><div id="result"></div><a id="download" href="/download">Download</a><script>document.cookie='session=private';document.querySelector('#result').textContent='dynamic ready';</script></body></html>`)
	}))
	defer server.Close()
	b, err := New(Options{Binary: binary, Extractor: fixtureExtractor{}, NoSandbox: os.Getenv("AGENTX_IT_BROWSER_NO_SANDBOX") == "1"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.NewSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := func(a core.Action) *core.Result {
		t.Helper()
		r, err := s.Do(t.Context(), a)
		if err != nil {
			t.Fatalf("%s: %v", a.Operation, err)
		}
		return r
	}
	r := run(core.Action{Operation: core.Navigate, URL: server.URL})
	if r.Document == nil || !strings.Contains(r.Document.Text, "dynamic ready") || len(r.Controls) < 3 {
		t.Fatalf("dynamic snapshot: %+v", r)
	}
	run(core.Action{Operation: core.Fill, Selector: "#name", Value: "typed evidence"})
	r = run(core.Action{Operation: core.Click, Selector: "#apply"})
	if !strings.Contains(r.Document.Text, `<div id="result">typed evidence</div>`) {
		t.Fatal("click/fill did not update rendered DOM")
	}
	r = run(core.Action{Operation: core.Screenshot})
	if r.MediaType != "image/png" || len(r.Data) < 8 || string(r.Data[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("invalid screenshot")
	}
	r = run(core.Action{Operation: core.Download, Selector: "#download"})
	if string(r.Data) != "download evidence" {
		t.Fatalf("download: %+v", r)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Do(cancelled, core.Action{Operation: core.Read}); err == nil {
		t.Fatal("cancel ignored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Do(t.Context(), core.Action{Operation: core.Read}); err == nil {
		t.Fatal("closed session reusable")
	}
	other, err := b.NewSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Do(t.Context(), core.Action{Operation: core.Navigate, URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	deadline, cancelDeadline := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelDeadline()
	if _, err := other.Do(deadline, core.Action{Operation: core.Click, Selector: "#never-present"}); err == nil {
		t.Fatal("timed out click succeeded")
	}
	if _, err := other.Do(t.Context(), core.Action{Operation: core.Read}); err == nil {
		t.Fatal("interrupted session was left running")
	}
	limitedDriver, err := New(Options{Binary: binary, Extractor: fixtureExtractor{}, MaxResultBytes: 4, NoSandbox: b.o.NoSandbox})
	if err != nil {
		t.Fatal(err)
	}
	limited, err := limitedDriver.NewSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if _, err := limited.Do(t.Context(), core.Action{Operation: core.Navigate, URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Do(t.Context(), core.Action{Operation: core.Download, Selector: "#download"}); err == nil || !strings.Contains(err.Error(), "download exceeds limit") {
		t.Fatalf("download cause lost during cleanup: %v", err)
	}
	if _, err := limited.Do(t.Context(), core.Action{Operation: core.Read}); err == nil {
		t.Fatal("oversized download left session running")
	}
}
