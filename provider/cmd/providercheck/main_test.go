package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/acceptance"
)

func TestRunWithoutLive(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	code := run(t.Context(), []string{"-provider=openai", "-model=fixture", "-media=/a/nonexistent/private/file"}, &out)
	var report acceptance.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if code == 0 || report.Status != "unverified" || report.ErrorCode != "live_not_enabled" {
		t.Fatalf("code=%d report=%+v", code, report)
	}
	if strings.Contains(out.String(), "private") {
		t.Fatal("fixture path leaked")
	}
}

func TestRunInvalidArguments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, arg string }{
		{name: "unknown flag", arg: "-secret-key=must-not-echo"},
		{name: "invalid timeout", arg: "-timeout=31m"},
		{name: "positional", arg: "must-not-echo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if code := run(t.Context(), []string{tc.arg}, &out); code == 0 || strings.Contains(out.String(), "must-not-echo") || !strings.Contains(out.String(), "invalid_arguments") {
				t.Fatalf("code=%d output=%s", code, &out)
			}
		})
	}
}

func TestReadMedia(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := readMedia(path, "text/plain")
	if err != nil || p.Filename != "fixture.txt" || string(p.Data) != "fixture" {
		t.Fatalf("part=%+v err=%v", p, err)
	}
	if _, err := readMedia(path, ""); err == nil {
		t.Fatal("missing MIME type accepted")
	}
}
