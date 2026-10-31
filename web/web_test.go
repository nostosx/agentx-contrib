package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractLinksAndHiddenContent(t *testing.T) {
	e, err := NewExtractor(ExtractOptions{FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Extract(t.Context(), "https://example.com/docs/", []byte(`<html><head><title>Guide</title><style>hidden style</style></head><body><h1>Guide</h1><p>Useful body <a href="../source">source</a></p><script>hidden script</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Links) != 1 || d.Links[0].URL != "https://example.com/source" || strings.Contains(d.Text, "hidden") || !strings.Contains(d.Markdown, "https://example.com/source") {
		t.Fatalf("extraction: %+v", d)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Extract(ctx, "https://example.com", nil); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestReaderRedirectCharsetAndLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
			fmt.Fprint(w, "<html><body><p>caf\xe9</p></body></html>")
		case "/large":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("x", 300))
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "binary")
		}
	}))
	defer server.Close()
	e, _ := NewExtractor(ExtractOptions{FullPage: true})
	r, err := NewReader(Options{Client: server.Client(), Extractor: e, MaxBodyBytes: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	doc, err := r.Read(t.Context(), server.URL+"/redirect")
	if err != nil || !strings.Contains(doc.Text, "café") || !strings.HasSuffix(doc.URL, "/page") {
		t.Fatalf("redirect/charset: %+v %v", doc, err)
	}
	for _, p := range []string{"/large", "/binary"} {
		if _, err := r.Read(t.Context(), server.URL+p); err == nil {
			t.Fatal("unbounded/unsupported response accepted", p)
		}
	}
	restricted, err := NewReader(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	if _, err := restricted.Read(t.Context(), server.URL); err == nil {
		t.Fatal("private URL accepted by default")
	}
}
